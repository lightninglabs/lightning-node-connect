package mobile

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/golang/protobuf/proto"
	"github.com/lightninglabs/faraday/frdrpc"
	"github.com/lightninglabs/lightning-node-connect/core"
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
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon-bakery.v2/bakery/checkers"
	"gopkg.in/macaroon.v2"
)

type mobileClient struct {
	lndConn *grpc.ClientConn

	statusChecker func() mailbox.ClientStatus

	mac *macaroon.Macaroon

	registry map[string]func(context.Context, *grpc.ClientConn,
		string, func(string, error))
}

func newMobileClient() *mobileClient {
	return &mobileClient{
		lndConn: nil,
		registry: make(map[string]func(context.Context,
			*grpc.ClientConn, string, func(string, error))),
	}
}

type stubPackageRegistration func(map[string]func(context.Context,
	*grpc.ClientConn, string, func(string, error)))

type NativeCallback interface {
	SendResult(json string)
}

var (
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
		frdrpc.RegisterFaradayServerJSONCallbacks,
	}

	perms = core.GetAllMethodPermissions()

	jsonCBRegex = regexp.MustCompile(`(\w+)\.(\w+)\.(\w+)`)

	m *mobileClient

	registry = make(map[string]func(context.Context,
		*grpc.ClientConn, string, func(string, error)))

	localPrivCreateCallback  NativeCallback
	remoteKeyReceiveCallback NativeCallback
	authDataCallback         NativeCallback
)

// InitLNC sets up everything required for LNC to run including
// signal interceptor, logs, and an instance of the mobile client.
func InitLNC(debugLevel string) error {
	// set debug level to 'info' if not specified
	if debugLevel == "" {
		debugLevel = "info"
	}

	// Hook interceptor for os signals.
	shutdownInterceptor, err := signal.Intercept()
	if err != nil {
		return err
	}

	logWriter := build.NewRotatingLogWriter()
	SetupLoggers(logWriter, shutdownInterceptor)

	err = build.ParseAndSetDebugLevels(debugLevel, logWriter)
	if err != nil {
		return err
	}

	m = newMobileClient()

	for _, registration := range registrations {
		registration(registry)
	}

	log.Debugf("Mobile client ready for connecting")
	return nil
}

// ConnectServer creates a connection from the client to the
// mailbox server.
func ConnectServer(mailboxServer string, isDevServer bool,
	pairingPhrase string, localStatic string, remoteStatic string) error {

	// Check that the correct arguments and config combinations have been
	// provided.
	err := validateArgs(mailboxServer, localStatic, remoteStatic)
	if err != nil {
		return err
	}

	// Parse the key arguments.
	localPriv, remotePub, err := parseKeys(localStatic, remoteStatic)
	if err != nil {
		return err
	}

	// Disable TLS verification for the REST connections if this is a dev
	// server.
	if isDevServer {
		defaultHTTPTransport := http.DefaultTransport.(*http.Transport)
		defaultHTTPTransport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	statusChecker, lndConnect, err := core.MailboxRPCConnection(
		mailboxServer, pairingPhrase, localPriv, remotePub,
		func(key *btcec.PublicKey) error {
			remoteKeyReceiveCallback.SendResult(hex.EncodeToString(
				key.SerializeCompressed(),
			))

			return nil
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

			m.mac = mac

			authDataCallback.SendResult(string(data))

			return nil
		},
	)

	if err != nil {
		return err
	}

	m.statusChecker = statusChecker
	m.lndConn, err = lndConnect()
	if err != nil {
		return err
	}

	log.Debugf("Mobile client connected to RPC")
	return nil
}

// IsConnected returns whether or not there is an active connection.
func IsConnected() bool {
	return m.lndConn != nil
}

// Disconnect closes the RPC connection.
func Disconnect() {
	if m.lndConn != nil {
		if err := m.lndConn.Close(); err != nil {
			log.Errorf("Error closing RPC connection: %v", err)
		}
		m.lndConn = nil
	}
}

// Status returns the status of the LNC RPC connection.
func Status() string {
	if m.statusChecker == nil {
		return ""
	}

	return m.statusChecker().String()
}

// RegisterLocalPrivCreateCallback sets up the native callbacks upon
// creation of local private key.
func RegisterLocalPrivCreateCallback(c NativeCallback) {
	localPrivCreateCallback = c
}

// RegisterRemoteKeyReceiveCallback sets up the native callbacks upon
// receiving the remote key from the server.
func RegisterRemoteKeyReceiveCallback(c NativeCallback) {
	remoteKeyReceiveCallback = c
}

// RegisterAuthDataCallback sets up the native callbacks upon
// receiving auth data.
func RegisterAuthDataCallback(c NativeCallback) {
	authDataCallback = c
}

// InvokeRPC makes a synchronous RPC call.
func InvokeRPC(rpcName string, requestJSON string, c NativeCallback) error {
	if rpcName == "" {
		return fmt.Errorf("param rpcName required")
	}

	if requestJSON == "" {
		return fmt.Errorf("param requestJSON required")
	}

	if m.lndConn == nil {
		return fmt.Errorf("RPC connection not ready")
	}

	method, ok := registry[rpcName]
	if !ok {
		return fmt.Errorf("rpc with name " + rpcName + " not found")
	}

	go func() {
		log.Infof("Calling '%s' on RPC with request %s", rpcName, requestJSON)
		cb := func(resultJSON string, err error) {
			if err != nil {
				c.SendResult(err.Error())
			} else {
				c.SendResult(resultJSON)
			}
		}
		ctx := context.Background()
		method(ctx, m.lndConn, requestJSON, cb)
		<-ctx.Done()
	}()

	return nil
}

// GetExpiry returns the expiration time of the connection macaroon.
func GetExpiry() (string, error) {
	if m.mac == nil {
		return "", fmt.Errorf("macaroon not obtained yet. GetExpiry should " +
			"only be called once the connection is complete")
	}

	expiry, found := checkers.ExpiryTime(nil, m.mac.Caveats())
	if !found {
		return "", fmt.Errorf("expiry not found")
	}

	return string(rune(expiry.Unix())), nil
}

// IsReadOnly returns whether or not the connection macaroon is read-only.
func IsReadOnly() bool {
	if m.mac == nil {
		log.Errorf("macaroon not obtained yet. IsReadOnly should " +
			"only be called once the connection is complete")
		return false
	}

	macOps, err := extractMacaroonOps(m.mac)
	if err != nil {
		log.Errorf("could not extract macaroon ops: %v", err)
		return false
	}

	// Check that the macaroon contains each of the required permissions
	// for the given URI.
	return isReadOnly(macOps)
}

// HasPermissions returns whether or not the connection macaroon
// has a specificed permission.
func HasPermissions(permission string) bool {
	if permission == "" {
		return false
	}

	if m.mac == nil {
		log.Errorf("macaroon not obtained yet. HasPermissions should " +
			"only be called once the connection is complete")
		return false
	}

	// Convert JSON callback to grpc URI. JSON callbacks are of the form:
	// `lnrpc.Lightning.WalletBalance` and the corresponding grpc URI is of
	// the form: `/lnrpc.Lightning/WalletBalance`. So to convert the one to
	// the other, we first convert all the `.` into `/`. Then we replace the
	// first `/` back to a `.` and then we prepend the result with a `/`.
	uri := jsonCBRegex.ReplaceAllString(permission, "/$1.$2/$3")

	ops, ok := perms[uri]
	if !ok {
		log.Errorf("uri %s not found in known permissions list", uri)
		return false
	}

	macOps, err := extractMacaroonOps(m.mac)
	if err != nil {
		log.Errorf("could not extract macaroon ops: %v", err)
		return false
	}

	// Check that the macaroon contains each of the required permissions
	// for the given URI.
	return hasPermissions(macOps, ops)
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
func hasPermissions(macOps []*lnrpc.Op, requiredOps []bakery.Op) bool {
	// Create a lookup map of the macaroon operations.
	macOpsMap := make(map[string]map[string]bool)
	for _, op := range macOps {
		macOpsMap[op.Entity] = make(map[string]bool)

		for _, action := range op.Actions {
			macOpsMap[op.Entity][action] = true
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
func validateArgs(mailboxServer, localPrivKey, remotePubKey string) error {
	if mailboxServer == "" {
		return errors.New("invalid use of ConnectServer, " +
			"need parameter mailboxServer")
	}

	if remotePubKey != "" && localPrivKey == "" {
		return errors.New("cannot set remote pub key if local priv " +
			"key is not also set")
	}

	return nil
}

// parseKeys parses the given keys from their string format and calls callback
// functions where appropriate. NOTE: This function assumes that the parameter
// combinations have been checked by validateArgs.
func parseKeys(localPrivKey, remotePubKey string) (
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

		localPrivCreateCallback.SendResult(
			hex.EncodeToString(privKey.Serialize()),
		)

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
