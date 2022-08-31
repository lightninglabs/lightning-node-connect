package main

import (
	"net"

	faraday "github.com/lightninglabs/faraday/frdrpcserver/perms"
	loopd "github.com/lightninglabs/loop/loopd/perms"
	poold "github.com/lightninglabs/pool/perms"
	"github.com/lightningnetwork/lnd"
	"github.com/lightningnetwork/lnd/autopilot"
	"github.com/lightningnetwork/lnd/chainreg"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/autopilotrpc"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"github.com/lightningnetwork/lnd/lnrpc/devrpc"
	"github.com/lightningnetwork/lnd/lnrpc/invoicesrpc"
	"github.com/lightningnetwork/lnd/lnrpc/neutrinorpc"
	"github.com/lightningnetwork/lnd/lnrpc/peersrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"github.com/lightningnetwork/lnd/lnrpc/watchtowerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/wtclientrpc"
	"github.com/lightningnetwork/lnd/lntest/mock"
	"github.com/lightningnetwork/lnd/routing"
	"github.com/lightningnetwork/lnd/sweep"
	"gopkg.in/macaroon-bakery.v2/bakery"
)

var (
	// whiteListedMethods is a map of all lnd RPC methods that don't require
	// any macaroon authentication.
	whiteListedMethods = map[string][]bakery.Op{
		"/lnrpc.WalletUnlocker/GenSeed":        {},
		"/lnrpc.WalletUnlocker/InitWallet":     {},
		"/lnrpc.WalletUnlocker/UnlockWallet":   {},
		"/lnrpc.WalletUnlocker/ChangePassword": {},

		// The State service must be available at all times, even
		// before we can check macaroons, so we whitelist it.
		"/lnrpc.State/SubscribeState": {},
		"/lnrpc.State/GetState":       {},
	}
)

// getAllMethodPermissions returns a merged map of all litd's method
// permissions.
func getAllMethodPermissions() map[string][]bakery.Op {
	allPerms := make(map[string][]bakery.Op)

	lndMainPerms := lnd.MainRPCServerPermissions()
	for key, value := range lndMainPerms {
		allPerms[key] = value
	}

	for key, value := range whiteListedMethods {
		allPerms[key] = value
	}

	ss := lnrpc.RegisteredSubServers()
	for _, subServer := range ss {
		_, perms, err := subServer.NewGrpcHandler().CreateSubServer(
			&mockConfig{},
		)
		if err != nil {
			panic(err)
		}

		for key, value := range perms {
			allPerms[key] = value
		}
	}

	for key, value := range faraday.RequiredPermissions {
		allPerms[key] = value
	}
	for key, value := range loopd.RequiredPermissions {
		allPerms[key] = value
	}
	for key, value := range poold.RequiredPermissions {
		allPerms[key] = value
	}
	return allPerms
}

var _ lnrpc.SubServerConfigDispatcher = (*mockConfig)(nil)

// mockConfig implements lnrpc.SubServerConfigDispatcher. It provides th
// functionality required so that the lnrpc.GrpcHandler.CreateSubServer
// function can be called without panicking.
type mockConfig struct{}

// FetchConfig is a mock implementation of lnrpc.SubServerConfigDispatcher. It
// is used as a parameter to lnrpc.GrpcHandler.CreateSubServer and allows the
// function to be called without panicking. This is useful because
// CreateSubServer can be used to extract the permissions required by each
// registered subserver.
//
// TODO(elle): remove this once the sub-server permission lists in LND have been
// exported.
func (t *mockConfig) FetchConfig(subServerName string) (interface{}, bool) {
	switch subServerName {
	case "InvoicesRPC":
		return &invoicesrpc.Config{}, true
	case "WatchtowerClientRPC":
		return &wtclientrpc.Config{
			Resolver: func(_, _ string) (*net.TCPAddr, error) {
				return nil, nil
			},
		}, true
	case "AutopilotRPC":
		return &autopilotrpc.Config{
			Manager: &autopilot.Manager{},
		}, true
	case "ChainRPC":
		return &chainrpc.Config{
			ChainNotifier: &chainreg.NoChainBackend{},
		}, true
	case "DevRPC":
		return &devrpc.Config{}, true
	case "NeutrinoKitRPC":
		return &neutrinorpc.Config{}, true
	case "PeersRPC":
		return &peersrpc.Config{}, true
	case "RouterRPC":
		return &routerrpc.Config{
			Router: &routing.ChannelRouter{},
		}, true
	case "SignRPC":
		return &signrpc.Config{
			Signer: &mock.DummySigner{},
		}, true
	case "WalletKitRPC":
		return &walletrpc.Config{
			FeeEstimator: &chainreg.NoChainBackend{},
			Wallet:       &mock.WalletController{},
			KeyRing:      &mock.SecretKeyRing{},
			Sweeper:      &sweep.UtxoSweeper{},
			Chain:        &mock.ChainIO{},
		}, true
	case "WatchtowerRPC":
		return &watchtowerrpc.Config{}, true
	default:
		return nil, false
	}
}
