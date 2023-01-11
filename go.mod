module github.com/lightninglabs/lightning-node-connect

require (
	github.com/btcsuite/btcd/btcec/v2 v2.2.2
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/go-errors/errors v1.0.1
	github.com/golang/protobuf v1.5.2
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.5.0
	github.com/kkdai/bstream v1.0.0
	github.com/lightninglabs/aperture v0.1.18-beta
	github.com/lightninglabs/faraday v0.2.8-alpha.0.20220909105059-fea194ffb084
	github.com/lightninglabs/lightning-node-connect/hashmailrpc v1.0.2
	github.com/lightninglabs/loop v0.20.1-beta.0.20220916122221-9c3010150016
	github.com/lightninglabs/pool v0.5.8-alpha
	github.com/lightningnetwork/lnd v0.15.5-beta
	github.com/lightningnetwork/lnd/ticker v1.1.0
	github.com/lightningnetwork/lnd/tor v1.0.2
	github.com/stretchr/testify v1.8.0
	golang.org/x/crypto v0.0.0-20211215153901-e495a2d5b3d3
	google.golang.org/grpc v1.39.0
	google.golang.org/protobuf v1.27.1
	gopkg.in/macaroon-bakery.v2 v2.0.1
	gopkg.in/macaroon.v2 v2.1.0
	nhooyr.io/websocket v1.8.7
)

go 1.16
