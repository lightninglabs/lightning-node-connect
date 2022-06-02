module github.com/lightninglabs/lightning-node-connect/cmd/wasm-client

require (
	github.com/btcsuite/btcd/btcec/v2 v2.2.0
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/faraday v0.2.7-alpha.0.20220503144421-cd1e56982f09
	github.com/lightninglabs/lightning-node-connect v0.1.9-alpha.0.20220602120524-e9964c685b18
	github.com/lightninglabs/loop v0.18.0-beta.0.20220602162617-992538eed917
	github.com/lightninglabs/pool v0.5.6-alpha
	github.com/lightningnetwork/lnd v0.15.0-beta.rc3
	google.golang.org/grpc v1.39.0
)

go 1.16
