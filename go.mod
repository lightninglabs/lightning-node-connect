module github.com/lightninglabs/terminal-connect

require (
	github.com/btcsuite/btcd v0.21.0-beta.0.20210513141527-ee5896bad5be
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/go-errors/errors v1.0.1
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.5.0
	github.com/kkdai/bstream v0.0.0-20181106074824-b3251f7901ec
	github.com/lightninglabs/aperture v0.1.6-beta
	github.com/lightninglabs/terminal-connect/hashmailrpc v1.0.0
	github.com/lightningnetwork/lnd v0.13.0-beta.rc5.0.20210728112744-ebabda671786
	github.com/lightningnetwork/lnd/ticker v1.0.0
	github.com/stretchr/testify v1.7.0
	golang.org/x/crypto v0.0.0-20201002170205-7f63de1d35b0
	google.golang.org/grpc v1.39.0
	google.golang.org/protobuf v1.27.1
	nhooyr.io/websocket v1.8.7
)

replace git.schwanenlied.me/yawning/bsaes.git => github.com/Yawning/bsaes v0.0.0-20180720073208-c0276d75487e

replace github.com/lightninglabs/terminal-connect/hashmailrpc => ./hashmailrpc

replace github.com/lightninglabs/aperture => github.com/lightninglabs/aperture-mailbox v0.0.0-20210813150142-b26a468d46c9

go 1.16
