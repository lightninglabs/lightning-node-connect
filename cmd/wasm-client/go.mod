module github.com/lightninglabs/terminal-connect/cmd/wasm-client

require (
	github.com/btcsuite/btcd v0.21.0-beta.0.20210513141527-ee5896bad5be
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/grpc-ecosystem/grpc-gateway v1.16.0
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/loop v0.14.1-beta
	github.com/lightninglabs/pool v0.5.0-alpha
	github.com/lightninglabs/pool/auctioneerrpc v1.0.2
	github.com/lightninglabs/terminal-connect v0.0.0-20210728113920-e9de05e8c4ab
	github.com/lightningnetwork/lnd v0.13.0-beta.rc5.0.20210728112744-ebabda671786
	google.golang.org/grpc v1.39.0
)

// Needed until loop and pool update to the latest lnd version too.
replace github.com/lightningnetwork/lnd => github.com/lightningnetwork/lnd v0.13.0-beta.rc5.0.20210728112744-ebabda671786

replace github.com/lightninglabs/terminal-connect => ../../

replace github.com/lightninglabs/terminal-connect/hashmailrpc => ../../hashmailrpc

go 1.16
