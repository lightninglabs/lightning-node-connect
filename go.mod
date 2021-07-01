module github.com/lightninglabs/terminal-connect

require (
	github.com/btcsuite/btcd v0.21.0-beta.0.20210513141527-ee5896bad5be
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/golang/protobuf v1.5.2
	github.com/grpc-ecosystem/grpc-gateway v1.16.0
	github.com/jessevdk/go-flags v1.4.0
	github.com/kkdai/bstream v0.0.0-20181106074824-b3251f7901ec
	github.com/lightninglabs/loop v0.14.1-beta
	github.com/lightninglabs/pool v0.5.0-alpha
	github.com/lightninglabs/pool/auctioneerrpc v1.0.2
	github.com/lightningnetwork/lnd v0.13.0-beta.rc5.0.20210728112744-ebabda671786
	github.com/stretchr/testify v1.7.0
	golang.org/x/crypto v0.0.0-20201002170205-7f63de1d35b0
	google.golang.org/grpc v1.38.0
	google.golang.org/protobuf v1.26.0
	nhooyr.io/websocket v1.8.7
)

replace git.schwanenlied.me/yawning/bsaes.git => github.com/Yawning/bsaes v0.0.0-20180720073208-c0276d75487e

// Needed until loop and pool update to the latest lnd version too.
replace github.com/lightningnetwork/lnd => github.com/lightningnetwork/lnd v0.13.0-beta.rc5.0.20210728112744-ebabda671786

go 1.16
