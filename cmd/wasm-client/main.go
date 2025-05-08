//go:build js

package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"syscall/js"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/jessevdk/go-flags"
	faraday "github.com/lightninglabs/faraday/frdrpcserver/perms"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightninglabs/lightning-terminal/litrpc"
	"github.com/lightninglabs/lightning-terminal/perms"
	"github.com/lightninglabs/loop/looprpc"
	"github.com/lightninglabs/pool/poolrpc"
	"github.com/lightninglabs/taproot-assets/taprpc"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/lightningnetwork/lnd/signal"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon-bakery.v2/bakery/checkers"
	"gopkg.in/macaroon.v2"
)

var (
	// initMu is to be used to guard global variables at initialisation
	// time.
	initMu sync.Mutex

	permsMgr *perms.Manager

	jsonCBRegex = regexp.MustCompile("(\\w+)\\.(\\w+)\\.(\\w+)")
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Recovered in f: %v", r)
			debug.PrintStack()
		}
	}()

	// Initialise any global variables if they have not yet been
	// initialised.
	if err := initGlobals(); err != nil {
		exit(err)
	}

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

	logConfig := build.DefaultLogConfig()
	logWriter := build.NewRotatingLogWriter()
	logMgr := build.NewSubLoggerManager(build.NewDefaultLogHandlers(
		logConfig, logWriter,
	)...)
	SetupLoggers(logMgr, shutdownInterceptor)

	err = build.ParseAndSetDebugLevels(cfg.DebugLevel, logMgr)
	if err != nil {
		exit(err)
	}

	wc, err := newWasmClient(&cfg)
	if err != nil {
		exit(fmt.Errorf("config validation error: %v", err))
	}

	// Setup JS callbacks.
	callbacks := make(map[string]interface{})
	callbacks["wasmClientIsReady"] = js.FuncOf(wc.IsReady)
	callbacks["wasmClientConnectServer"] = js.FuncOf(wc.ConnectServer)
	callbacks["wasmClientIsConnected"] = js.FuncOf(wc.IsConnected)
	callbacks["wasmClientDisconnect"] = js.FuncOf(wc.Disconnect)
	callbacks["wasmClientInvokeRPC"] = js.FuncOf(wc.InvokeRPC)
	callbacks["wasmClientStatus"] = js.FuncOf(wc.Status)
	callbacks["wasmClientGetExpiry"] = js.FuncOf(wc.GetExpiry)
	callbacks["wasmClientHasPerms"] = js.FuncOf(wc.HasPermissions)
	callbacks["wasmClientIsReadOnly"] = js.FuncOf(wc.IsReadOnly)
	callbacks["wasmClientIsCustom"] = js.FuncOf(wc.IsCustom)

	// Check if a JS object for the namespace already exists on the global
	// scope.
	nsObj := js.Global().Get(cfg.NameSpace)
	if !isEmptyObject(nsObj) {
		// If it exists, set the callbacks on the existing object. This
		// prevents overwriting the existing object, removing any
		// vars/funcs that may have been set by the user.
		for cb, name := range callbacks {
			nsObj.Set(cb, name)
		}
	} else {
		// If not, create the JS object and set the callbacks.
		js.Global().Set(cfg.NameSpace, js.ValueOf(callbacks))
	}

	for _, registration := range litrpc.Registrations {
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

// initGlobals initialises any global variables that have not yet been
// initialised.
func initGlobals() error {
	initMu.Lock()
	defer initMu.Unlock()

	if permsMgr != nil {
		return nil
	}

	var err error
	permsMgr, err = perms.NewManager(true)
	if err != nil {
		return err
	}

	permsMgr.RegisterSubServer(
		"taproot-assets", taprpc.RequiredPermissions,
		taprpc.MacaroonWhitelist(false, false, false, false),
	)
	permsMgr.RegisterSubServer("loop", looprpc.RequiredPermissions, nil)
	permsMgr.RegisterSubServer("pool", poolrpc.RequiredPermissions, nil)
	permsMgr.RegisterSubServer("faraday", faraday.RequiredPermissions, nil)

	return err
}

type wasmClient struct {
	cfg *config

	lndConn *grpc.ClientConn

	statusChecker func() mailbox.ClientStatus

	mac *macaroon.Macaroon

	registry map[string]func(context.Context, *grpc.ClientConn,
		string, func(string, error))
}

func newWasmClient(cfg *config) (*wasmClient, error) {
	if cfg.NameSpace == "" {
		return nil, fmt.Errorf("a non-empty namespace is required")
	}

	return &wasmClient{
		cfg:     cfg,
		lndConn: nil,
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
	if len(args) != 5 {
		return js.ValueOf("invalid use of wasmClientConnectServer, " +
			"need 5 parameters: server, isDevServer, " +
			"pairingPhrase, localStaticPrivKey, remoteStaticPubKey")
	}

	mailboxServer := args[0].String()
	isDevServer := args[1].Bool()
	pairingPhrase := args[2].String()
	localStatic := args[3].String()
	remoteStatic := args[4].String()

	// Check that the correct arguments and config combinations have been
	// provided.
	err := validateArgs(w.cfg, localStatic, remoteStatic)
	if err != nil {
		exit(err)
	}

	// Parse the key arguments.
	localPriv, remotePub, err := parseKeys(
		w.cfg.OnLocalPrivCreate, localStatic, remoteStatic,
	)
	if err != nil {
		exit(err)
	}

	// Disable TLS verification for the REST connections if this is a dev
	// server.
	if isDevServer {
		defaultHttpTransport := http.DefaultTransport.(*http.Transport)
		defaultHttpTransport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	// Since the connection function is blocking, we need to spin it off
	// in another goroutine here. See https://pkg.go.dev/syscall/js#FuncOf.
	go func() {
		var err error
		statusChecker, lndConnect, err := mailbox.NewClientWebsocketConn(
			mailboxServer, pairingPhrase, localPriv, remotePub,
			func(key *btcec.PublicKey) error {
				return callJsCallback(
					w.cfg.OnRemoteKeyReceive,
					hex.EncodeToString(
						key.SerializeCompressed(),
					),
				)
			}, func(data []byte) error {
				parts := strings.Split(string(data), ": ")
				if len(parts) != 2 || parts[0] != "Macaroon" {
					return fmt.Errorf("authdata does " +
						"not contain a macaroon")
				}

				macBytes, err := hex.DecodeString(parts[1])
				if err != nil {
					return err
				}

				mac := &macaroon.Macaroon{}
				err = mac.UnmarshalBinary(macBytes)
				if err != nil {
					return fmt.Errorf("unable to decode "+
						"macaroon: %v", err)
				}

				w.mac = mac

				return callJsCallback(
					w.cfg.OnAuthData, string(data),
				)
			},
		)
		if err != nil {
			exit(err)
		}

		w.statusChecker = statusChecker
		w.lndConn, err = lndConnect()
		if err != nil {
			exit(err)
		}

		log.Debugf("WASM client connected to RPC")
	}()

	return nil
}

func (w *wasmClient) IsConnected(_ js.Value, _ []js.Value) interface{} {
	return js.ValueOf(w.lndConn != nil)
}

// Disconnect disconnects the client, and closes the connection.
// The first argument passed should be a onDisconnect callback, which will be
// invoked once the client has disconnected.
func (w *wasmClient) Disconnect(_ js.Value, args []js.Value) interface{} {
	if w.lndConn != nil {
		// We launch the closure of the connection in a goroutine to
		// avoid that the JS websocket freezes and blocks while closing.
		go func() {
			if err := w.lndConn.Close(); err != nil {
				log.Errorf("Error closing RPC connection: %v",
					err)
			}
			w.lndConn = nil

			// We expect the first arg to be the onDisconnect
			// callback
			if len(args) > 0 && args[0].Type() == js.TypeFunction {
				callback := args[0]

				// Call the onDisconnect callback.
				callback.Invoke()
			}
		}()
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
		log.Errorf("Invalid use of wasmClientInvokeRPC, need 3 " +
			"parameters: rpcName, request, callback")

		return js.ValueOf("invalid use of wasmClientInvokeRPC, " +
			"need 3 parameters: rpcName, request, callback")
	}

	if w.lndConn == nil {
		log.Errorf("Attempted to invoke RPC but connection is not " +
			"ready")

		return js.ValueOf("RPC connection not ready")
	}

	rpcName := args[0].String()
	requestJSON := args[1].String()
	jsCallback := args[len(args)-1:][0]

	method, ok := w.registry[rpcName]
	if !ok {
		log.Errorf("RPC method '%s' not found in registry", rpcName)

		return js.ValueOf("rpc with name " + rpcName + " not found")
	}

	go func() {
		log.Infof("Calling '%s' on RPC with request %s",
			rpcName, requestJSON)
		cb := func(resultJSON string, err error) {
			if err != nil {
				log.Errorf("RPC '%s' failed: %v", rpcName, err)
				jsCallback.Invoke(js.ValueOf(err.Error()))
			} else {
				log.Tracef("RPC '%s' succeeded with result: %s",
					rpcName, resultJSON)
				jsCallback.Invoke(js.ValueOf(resultJSON))
			}
		}
		ctx := context.Background()
		method(ctx, w.lndConn, requestJSON, cb)
		<-ctx.Done()
	}()
	return nil
}

func (w *wasmClient) GetExpiry(_ js.Value, _ []js.Value) interface{} {
	if w.mac == nil {
		log.Errorf("macaroon not obtained yet. GetExpiry should " +
			"only be called once the connection is complete")
		return nil
	}

	expiry, found := checkers.ExpiryTime(nil, w.mac.Caveats())
	if !found {
		return nil
	}

	return js.ValueOf(expiry.Unix())
}

func (w *wasmClient) IsReadOnly(_ js.Value, _ []js.Value) interface{} {
	if w.mac == nil {
		log.Errorf("macaroon not obtained yet. IsReadOnly should " +
			"only be called once the connection is complete")
		return js.ValueOf(false)
	}

	macOps, err := extractMacaroonOps(w.mac)
	if err != nil {
		log.Errorf("could not extract macaroon ops: %v", err)
		return js.ValueOf(false)
	}

	// Check that the macaroon contains each of the required permissions
	// for the given URI.
	return js.ValueOf(isReadOnly(macOps))
}

func (w *wasmClient) IsCustom(_ js.Value, _ []js.Value) interface{} {
	if w.mac == nil {
		log.Errorf("macaroon not obtained yet. IsCustom should " +
			"only be called once the connection is complete")
		return js.ValueOf(false)
	}

	macOps, err := extractMacaroonOps(w.mac)
	if err != nil {
		log.Errorf("could not extract macaroon ops: %v", err)
		return js.ValueOf(false)
	}

	// We consider a session type to be "custom" if it has any permissions
	// with the "uri" entity.
	var isCustom bool
	for _, op := range macOps {
		if op.Entity == macaroons.PermissionEntityCustomURI {
			isCustom = true
			break
		}
	}

	return js.ValueOf(isCustom)
}

func (w *wasmClient) HasPermissions(_ js.Value, args []js.Value) interface{} {
	if len(args) != 1 {
		return js.ValueOf(false)
	}

	if w.mac == nil {
		log.Errorf("macaroon not obtained yet. HasPermissions should " +
			"only be called once the connection is complete")
		return js.ValueOf(false)
	}

	// Convert JSON callback to grpc URI. JSON callbacks are of the form:
	// `lnrpc.Lightning.WalletBalance` and the corresponding grpc URI is of
	// the form: `/lnrpc.Lightning/WalletBalance`. So to convert the one to
	// the other, we first convert all the `.` into `/`. Then we replace the
	// first `/` back to a `.` and then we prepend the result with a `/`.
	uri := jsonCBRegex.ReplaceAllString(args[0].String(), "/$1.$2/$3")

	ops, ok := permsMgr.URIPermissions(uri)
	if !ok {
		log.Errorf("uri %s not found in known permissions list", uri)
		return js.ValueOf(false)
	}

	macOps, err := extractMacaroonOps(w.mac)
	if err != nil {
		log.Errorf("could not extract macaroon ops: %v", err)
		return js.ValueOf(false)
	}

	// Check that the macaroon contains each of the required permissions
	// for the given URI.
	return js.ValueOf(hasPermissions(uri, macOps, ops))
}

// extractMacaroonOps is a helper function that extracts operations from the
// ID of a macaroon.
func extractMacaroonOps(mac *macaroon.Macaroon) ([]*lnrpc.Op, error) {
	rawID := mac.Id()
	if rawID[0] != byte(bakery.LatestVersion) {
		return nil, fmt.Errorf("invalid macaroon version: %x", rawID)
	}

	decodedID := &lnrpc.MacaroonId{}
	idProto := rawID[1:]
	err := proto.Unmarshal(idProto, decodedID)
	if err != nil {
		return nil, fmt.Errorf("unable to decode macaroon: %v", err)
	}

	return decodedID.Ops, nil
}

// isReadOnly returns true if the given operations only contain "read" actions.
func isReadOnly(ops []*lnrpc.Op) bool {
	for _, op := range ops {
		for _, action := range op.Actions {
			if action != "read" {
				return false
			}
		}
	}

	return true
}

// hasPermissions returns true if all the operations in requiredOps can also be
// found in macOps.
func hasPermissions(uri string, macOps []*lnrpc.Op,
	requiredOps []bakery.Op) bool {

	// Create a lookup map of the macaroon operations.
	macOpsMap := make(map[string]map[string]bool)
	for _, op := range macOps {
		macOpsMap[op.Entity] = make(map[string]bool)

		for _, action := range op.Actions {
			macOpsMap[op.Entity][action] = true

			// We account here for the special case where the
			// operation gives access to an entire URI. This is the
			// case when the Entity is equal to the "uri" keyword
			// and when the Action is equal to the URI that access
			// is being granted to.
			if op.Entity == macaroons.PermissionEntityCustomURI &&
				action == uri {

				return true
			}
		}
	}

	// For each of the required operations, we ensure that the macaroon also
	// contains the operation.
	for _, op := range requiredOps {
		macEntity, ok := macOpsMap[op.Entity]
		if !ok {
			return false
		}

		if !macEntity[op.Action] {
			return false
		}
	}

	return true
}

// validateArgs checks that the correct keys and callback functions have been
// provided.
func validateArgs(cfg *config, localPrivKey, remotePubKey string) error {
	if remotePubKey != "" && localPrivKey == "" {
		return errors.New("cannot set remote pub key if local priv " +
			"key is not also set")
	}

	if localPrivKey == "" && cfg.OnLocalPrivCreate == "" {
		return errors.New("OnLocalPrivCreate must be defined if a " +
			"local key is not provided")
	}

	if remotePubKey == "" && cfg.OnRemoteKeyReceive == "" {
		return errors.New("OnRemoteKeyReceive must be defined if a " +
			"remote key is not provided")
	}

	return nil
}

// parseKeys parses the given keys from their string format and calls callback
// functions where appropriate. NOTE: This function assumes that the parameter
// combinations have been checked by validateArgs.
func parseKeys(onLocalPrivCreate, localPrivKey, remotePubKey string) (
	keychain.SingleKeyECDH, *btcec.PublicKey, error) {

	var (
		localStaticKey  keychain.SingleKeyECDH
		remoteStaticKey *btcec.PublicKey
	)
	switch {

	// This is a new session for which a local key has not yet been derived,
	// so we generate a new key and call the onLocalPrivCreate callback so
	// that this key can be persisted.
	case localPrivKey == "" && remotePubKey == "":
		privKey, err := btcec.NewPrivateKey()
		if err != nil {
			return nil, nil, err
		}
		localStaticKey = &keychain.PrivKeyECDH{PrivKey: privKey}

		err = callJsCallback(
			onLocalPrivCreate,
			hex.EncodeToString(privKey.Serialize()),
		)
		if err != nil {
			return nil, nil, err
		}

	// A local private key has been provided, so parse it.
	case remotePubKey == "":
		privKeyByte, err := hex.DecodeString(localPrivKey)
		if err != nil {
			return nil, nil, err
		}

		privKey, _ := btcec.PrivKeyFromBytes(privKeyByte)
		localStaticKey = &keychain.PrivKeyECDH{PrivKey: privKey}

	// Both local private key and remote public key have been provided,
	// so parse them both into the appropriate types.
	default:
		// Both local and remote are set.
		localPrivKeyBytes, err := hex.DecodeString(localPrivKey)
		if err != nil {
			return nil, nil, err
		}
		privKey, _ := btcec.PrivKeyFromBytes(localPrivKeyBytes)
		localStaticKey = &keychain.PrivKeyECDH{PrivKey: privKey}

		remoteKeyBytes, err := hex.DecodeString(remotePubKey)
		if err != nil {
			return nil, nil, err
		}

		remoteStaticKey, err = btcec.ParsePubKey(remoteKeyBytes)
		if err != nil {
			return nil, nil, err
		}
	}

	return localStaticKey, remoteStaticKey, nil
}

func callJsCallback(callbackName string, value string) error {
	// callbackName can be in the form of "namespace.callbackName" or just
	// "callbackName". If it is in the former form, we need to get the
	// namespace object and call the callback on that object.
	jsScope := js.Global()
	parts := strings.Split(callbackName, ".")
	if len(parts) > 1 {
		jsScope = jsScope.Get(parts[0])
		callbackName = parts[1]
	}

	retValue := jsScope.Call(callbackName, value)

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
