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
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"
	"google.golang.org/grpc"
	
	// Needed as a build time dependency only so we can pull in the proto
	// files to generate the stubs.
	_ "github.com/lightninglabs/loop/looprpc"
	_ "github.com/lightninglabs/pool/auctioneerrpc"
	_ "github.com/lightninglabs/pool/poolrpc"
)

var (
	lndConn *grpc.ClientConn

	registry = make(map[string]func(ctx context.Context,
		conn *grpc.ClientConn, reqJSON string,
		callback func(string, error)))
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
	js.Global().Set("wasmClientInvokeRPC", js.FuncOf(wasmClientInvokeRPC))
	RegisterLightningJSONCallbacks(registry)
	RegisterStateJSONCallbacks(registry)
	RegisterVersionerJSONCallbacks(registry)
	RegisterSwapClientJSONCallbacks(registry)
	RegisterTraderJSONCallbacks(registry)

	cfg := config{}

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

	// Disable TLS verification for the REST connections if this is a dev
	// server.
	if cfg.MailboxDevServer {
		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
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

	conn, err := mailboxRPCConnection(&cfg)
	if err != nil {
		exit(err)
	}

	lndConn = conn

	log.Debugf("WASM client ready and connected to RPC")

	select {
	case <-shutdownInterceptor.ShutdownChannel():
		log.Debugf("Shutting down WASM client")
		if err := conn.Close(); err != nil {
			log.Errorf("Error closing RPC connection: %v", err)
		}
		log.Debugf("Shutdown of WASM client complete")
	}
}

func wasmClientIsReady(_ js.Value, _ []js.Value) interface{} {
	return js.ValueOf(lndConn != nil)
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