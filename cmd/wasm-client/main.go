//go:build js
// +build js

package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"syscall/js"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/jessevdk/go-flags"
	"github.com/lightninglabs/faraday/frdrpc"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightninglabs/loop/looprpc"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/autopilotrpc"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"github.com/lightningnetwork/lnd/lnrpc/invoicesrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/lightningnetwork/lnd/lnrpc/verrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"github.com/lightningnetwork/lnd/lnrpc/watchtowerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/wtclientrpc"
	"github.com/lightningnetwork/lnd/signal"
	"google.golang.org/grpc"
)

type stubPackageRegistration func(map[string]func(context.Context,
	*grpc.ClientConn, string, func(string, error)))

var registrations = []stubPackageRegistration{
	lnrpc.RegisterLightningJSONCallbacks,
	lnrpc.RegisterStateJSONCallbacks,
	autopilotrpc.RegisterAutopilotJSONCallbacks,
	chainrpc.RegisterChainNotifierJSONCallbacks,
	invoicesrpc.RegisterInvoicesJSONCallbacks,
	routerrpc.RegisterRouterJSONCallbacks,
	signrpc.RegisterSignerJSONCallbacks,
	verrpc.RegisterVersionerJSONCallbacks,
	walletrpc.RegisterWalletKitJSONCallbacks,
	watchtowerrpc.RegisterWatchtowerJSONCallbacks,
	wtclientrpc.RegisterWatchtowerClientJSONCallbacks,
	looprpc.RegisterSwapClientJSONCallbacks,
	poolrpc.RegisterTraderJSONCallbacks,
	frdrpc.RegisterFaradayServerJSONCallbacks,
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Recovered in f: %v", r)
			debug.PrintStack()
		}
	}()

	// Parse command line flags.
	cfg := config{}
	parser := flags.NewParser(&cfg, flags.Default)
	parser.SubcommandsOptional = true

	_, err := parser.Parse()
	if e, ok := err.(*flags.Error); ok && e.Type == flags.ErrHelp {
		exit(err)
	}
	if err != nil {
		exit(err)
	}

	// Hook interceptor for os signals.
	shutdownInterceptor, err := signal.Intercept()
	if err != nil {
		exit(err)
	}

	logWriter := build.NewRotatingLogWriter()
	SetupLoggers(logWriter, shutdownInterceptor)

	err = build.ParseAndSetDebugLevels(cfg.DebugLevel, logWriter)
	if err != nil {
		exit(err)
	}

	wc, err := newWasmClient(&cfg)
	if err != nil {
		exit(fmt.Errorf("config validation error: %v", err))
	}

	// Setup JS callbacks.
	callbacks := js.ValueOf(make(map[string]interface{}))
	callbacks.Set("wasmClientIsReady", js.FuncOf(wc.IsReady))
	callbacks.Set("wasmClientConnectServer", js.FuncOf(wc.ConnectServer))
	callbacks.Set("wasmClientIsConnected", js.FuncOf(wc.IsConnected))
	callbacks.Set("wasmClientDisconnect", js.FuncOf(wc.Disconnect))
	callbacks.Set("wasmClientInvokeRPC", js.FuncOf(wc.InvokeRPC))
	callbacks.Set("wasmClientStatus", js.FuncOf(wc.Status))
	js.Global().Set(cfg.NameSpace, callbacks)

	for _, registration := range registrations {
		registration(wc.registry)
	}

	log.Debugf("WASM client ready for connecting")

	select {
	case <-shutdownInterceptor.ShutdownChannel():
		log.Debugf("Shutting down WASM client")
		_ = wc.Disconnect(js.ValueOf(nil), nil)
		log.Debugf("Shutdown of WASM client complete")
	}
}

type wasmClient struct {
	cfg *config

	lndConn *grpc.ClientConn

	statusChecker func() mailbox.ConnStatus

	localStaticKey  keychain.SingleKeyECDH
	remoteStaticKey *btcec.PublicKey

	registry map[string]func(context.Context, *grpc.ClientConn,
		string, func(string, error))
}

func newWasmClient(cfg *config) (*wasmClient, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	var (
		localStaticKey  keychain.SingleKeyECDH
		remoteStaticKey *btcec.PublicKey
	)
	switch {
	case cfg.LocalPrivate == "" && cfg.RemotePublic == "":
		privKey, err := btcec.NewPrivateKey()
		if err != nil {
			return nil, err
		}
		localStaticKey = &keychain.PrivKeyECDH{PrivKey: privKey}

		err = callJsCallback(
			cfg.OnLocalPrivCreate,
			hex.EncodeToString(privKey.Serialize()),
		)
		if err != nil {
			return nil, err
		}

	case cfg.RemotePublic == "":
		privKeyByte, err := hex.DecodeString(cfg.LocalPrivate)
		if err != nil {
			return nil, err
		}

		privKey, _ := btcec.PrivKeyFromBytes(privKeyByte)
		localStaticKey = &keychain.PrivKeyECDH{PrivKey: privKey}

	default:
		// Both local and remote are set.
		localPrivKeyBytes, err := hex.DecodeString(cfg.LocalPrivate)
		if err != nil {
			return nil, err
		}
		privKey, _ := btcec.PrivKeyFromBytes(localPrivKeyBytes)
		localStaticKey = &keychain.PrivKeyECDH{PrivKey: privKey}

		remoteKeyBytes, err := hex.DecodeString(cfg.RemotePublic)
		if err != nil {
			return nil, err
		}

		remoteStaticKey, err = btcec.ParsePubKey(remoteKeyBytes)
		if err != nil {
			return nil, err
		}
	}

	return &wasmClient{
		cfg:             cfg,
		lndConn:         nil,
		localStaticKey:  localStaticKey,
		remoteStaticKey: remoteStaticKey,
		registry: make(map[string]func(context.Context,
			*grpc.ClientConn, string, func(string, error))),
	}, nil
}

func (w *wasmClient) IsReady(_ js.Value, _ []js.Value) interface{} {
	// This will always return true. So as soon as this method is called
	// successfully the JS part knows the WASM instance is fully started up
	// and ready to connect.
	return js.ValueOf(true)
}

func (w *wasmClient) ConnectServer(_ js.Value, args []js.Value) interface{} {
	if len(args) != 3 {
		return js.ValueOf("invalid use of wasmClientConnectServer, " +
			"need 3 parameters: server, isDevServer, pairingPhrase")
	}

	mailboxServer := args[0].String()
	isDevServer := args[1].Bool()
	pairingPhrase := args[2].String()

	// Disable TLS verification for the REST connections if this is a dev
	// server.
	if isDevServer {
		defaultHttpTransport := http.DefaultTransport.(*http.Transport)
		defaultHttpTransport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	var err error
	statusChecker, lndConnect, err := mailboxRPCConnection(
		mailboxServer, pairingPhrase, w.localStaticKey,
		w.remoteStaticKey, func(key *btcec.PublicKey) error {
			return callJsCallback(
				w.cfg.OnRemoteKeyReceive,
				hex.EncodeToString(key.SerializeCompressed()),
			)
		}, func(data []byte) error {
			return callJsCallback(
				w.cfg.OnAuthData, hex.EncodeToString(data),
			)
		},
	)
	if err != nil {
		exit(err)
	}

	w.statusChecker = statusChecker
	w.lndConn, err = lndConnect()

	log.Debugf("WASM client connected to RPC")

	return nil
}

func (w *wasmClient) IsConnected(_ js.Value, _ []js.Value) interface{} {
	return js.ValueOf(w.lndConn != nil)
}

func (w *wasmClient) Disconnect(_ js.Value, _ []js.Value) interface{} {
	if w.lndConn != nil {
		if err := w.lndConn.Close(); err != nil {
			log.Errorf("Error closing RPC connection: %v", err)
		}
	}

	return nil
}

func (w *wasmClient) Status(_ js.Value, _ []js.Value) interface{} {
	if w.statusChecker == nil {
		return nil
	}

	return js.ValueOf(w.statusChecker().String())
}

func (w *wasmClient) InvokeRPC(_ js.Value, args []js.Value) interface{} {
	if len(args) != 3 {
		return js.ValueOf("invalid use of wasmClientInvokeRPC, " +
			"need 3 parameters: rpcName, request, callback")
	}

	if w.lndConn == nil {
		return js.ValueOf("RPC connection not ready")
	}

	rpcName := args[0].String()
	requestJSON := args[1].String()
	jsCallback := args[len(args)-1:][0]

	method, ok := w.registry[rpcName]
	if !ok {
		return js.ValueOf("rpc with name " + rpcName + " not found")
	}

	go func() {
		log.Infof("Calling '%s' on RPC with request %s",
			rpcName, requestJSON)
		cb := func(resultJSON string, err error) {
			if err != nil {
				jsCallback.Invoke(js.ValueOf(err.Error()))
			} else {
				jsCallback.Invoke(js.ValueOf(resultJSON))
			}
		}
		ctx := context.Background()
		method(ctx, w.lndConn, requestJSON, cb)
		<-ctx.Done()
	}()
	return nil

}

func callJsCallback(callbackName string, value string) error {
	retValue := js.Global().Call(callbackName, value)

	if isEmptyObject(retValue) || isEmptyObject(retValue.Get("err")) {
		return nil
	}

	return fmt.Errorf(retValue.Get("err").String())
}

func isEmptyObject(value js.Value) bool {
	return value.IsNull() || value.IsUndefined()
}

func exit(err error) {
	// We use the fmt package for this error statement here instead of the
	// logger so that we can use this exit function before the logger has
	// been initialised since this would result in a panic.
	fmt.Printf("Error running wasm client: %v\n", err)
	os.Exit(1)
}
