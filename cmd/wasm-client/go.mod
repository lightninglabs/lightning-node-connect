module github.com/lightninglabs/lightning-node-connect/cmd/wasm-client

require (
	github.com/btcsuite/btcd/btcec/v2 v2.2.2
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/golang/protobuf v1.5.2
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/faraday v0.2.8-alpha.0.20220909105059-fea194ffb084
	github.com/lightninglabs/lightning-node-connect v0.1.9-alpha.0.20220602120524-e9964c685b18
	github.com/lightninglabs/loop v0.20.1-beta.0.20220916122221-9c3010150016
	github.com/lightninglabs/pool v0.5.8-alpha
	github.com/lightningnetwork/lnd v0.15.5-beta
	google.golang.org/grpc v1.39.0
	gopkg.in/macaroon-bakery.v2 v2.0.1
	gopkg.in/macaroon.v2 v2.1.0
)

replace github.com/lightninglabs/lightning-node-connect => ../../

replace github.com/lightninglabs/lightning-node-connect/hashmailrpc => ../../hashmailrpc

go 1.16
