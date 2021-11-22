module github.com/lightninglabs/lightning-node-connect/cmd/wasm-client

require (
	github.com/btcsuite/btcd v0.21.0-beta.0.20210513141527-ee5896bad5be
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/lightning-node-connect v0.0.0-20210728113920-e9de05e8c4ab
	github.com/lightninglabs/loop v0.15.0-beta.0.20210811082039-7ac6e26e90c4
	github.com/lightninglabs/pool v0.5.1-alpha.0.20210805073735-09c8937f2284
	github.com/lightninglabs/pool/auctioneerrpc v1.0.3 // indirect
	github.com/lightningnetwork/lnd v0.13.0-beta.rc5.0.20210812073038-5499a35987b7
	google.golang.org/grpc v1.39.0
)

replace github.com/lightninglabs/lightning-node-connect => ../../

replace github.com/lightninglabs/lightning-node-connect/hashmailrpc => ../../hashmailrpc

replace github.com/lightningnetwork/lnd/kvdb => github.com/lightningnetwork/lnd/kvdb v1.0.2-0.20210812073038-5499a35987b7

go 1.16
