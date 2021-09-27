// +build js

package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"runtime/debug"
	"syscall/js"

	"github.com/jessevdk/go-flags"
	"github.com/lightninglabs/loop/looprpc"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightningnetwork/lnd/build"
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

var (
	cfg     = config{}
	lndConn *grpc.ClientConn

	registry = make(map[string]func(context.Context, *grpc.ClientConn,
		string, func(string, error)))

	registrations = []stubPackageRegistration{
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
	}
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Debugf("Recovered in f: %v", r)
			debug.PrintStack()
		}
	}()

	// Setup JS callbacks.
	js.Global().Set("wasmClientIsReady", js.FuncOf(wasmClientIsReady))
	js.Global().Set(
		"wasmClientConnectServer", js.FuncOf(wasmClientConnectServer),
	)
	js.Global().Set(
		"wasmClientIsConnected", js.FuncOf(wasmClientIsConnected),
	)
	js.Global().Set("wasmClientDisconnect", js.FuncOf(wasmClientDisconnect))
	js.Global().Set("wasmClientInvokeRPC", js.FuncOf(wasmClientInvokeRPC))
	for _, registration := range registrations {
		registration(registry)
	}

	// Parse command line flags.
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

	log.Debugf("WASM client ready for connecting")

	select {
	case <-shutdownInterceptor.ShutdownChannel():
		log.Debugf("Shutting down WASM client")
		_ = wasmClientDisconnect(js.ValueOf(nil), nil)
		log.Debugf("Shutdown of WASM client complete")
	}
}

func wasmClientIsReady(_ js.Value, _ []js.Value) interface{} {
	// This will always return true. So as soon as this method is called
	// successfully the JS part knows the WASM instance is fully started up
	// and ready to connect.
	return js.ValueOf(true)
}

func wasmClientConnectServer(_ js.Value, args []js.Value) interface{} {
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
	lndConn, err = mailboxRPCConnection(mailboxServer, pairingPhrase)
	if err != nil {
		exit(err)
	}

	log.Debugf("WASM client connected to RPC")

	return nil
}

func wasmClientIsConnected(_ js.Value, _ []js.Value) interface{} {
	return js.ValueOf(lndConn != nil)
}

func wasmClientDisconnect(_ js.Value, _ []js.Value) interface{} {
	if lndConn != nil {
		if err := lndConn.Close(); err != nil {
			log.Errorf("Error closing RPC connection: %v", err)
		}
	}

	return nil
}

func wasmClientInvokeRPC(_ js.Value, args []js.Value) interface{} {
	if len(args) != 3 {
		return js.ValueOf("invalid use of wasmClientInvokeRPC, " +
			"need 3 parameters: rpcName, request, callback")
	}

	if lndConn == nil {
		return js.ValueOf("RPC connection not ready")
	}

	rpcName := args[0].String()
	requestJSON := args[1].String()
	jsCallback := args[len(args)-1:][0]

	method, ok := registry[rpcName]
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
		method(ctx, lndConn, requestJSON, cb)
		<-ctx.Done()
	}()
	return nil

}

func exit(err error) {
	log.Debugf("Error running wasm client: %v", err)
	os.Exit(1)
}
