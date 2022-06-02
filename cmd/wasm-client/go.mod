module github.com/lightninglabs/lightning-node-connect/cmd/wasm-client

require (
	github.com/btcsuite/btcd/btcec/v2 v2.2.0
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/lightning-node-connect v0.0.0-20210728113920-e9de05e8c4ab
	github.com/lightninglabs/loop v0.18.0-beta
	github.com/lightninglabs/pool v0.5.6-alpha
	github.com/lightningnetwork/lnd v0.15.0-beta.rc3
	google.golang.org/grpc v1.39.0
)

replace github.com/lightninglabs/lightning-node-connect => ../../

replace github.com/lightninglabs/lightning-node-connect/hashmailrpc => ../../hashmailrpc

go 1.16
