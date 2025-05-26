module github.com/lightninglabs/lightning-node-connect/mailbox

go 1.23.9

require (
	github.com/btcsuite/btcd/btcec/v2 v2.3.4
	github.com/btcsuite/btclog/v2 v2.0.1-0.20250110154127-3ae4bf1cb318
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.16.0
	github.com/kkdai/bstream v1.0.0
	github.com/lightninglabs/lightning-node-connect/gbn v1.0.0
	github.com/lightninglabs/lightning-node-connect/hashmailrpc v1.0.2
	github.com/lightningnetwork/lnd v0.19.0-beta
	github.com/lightningnetwork/lnd/tor v1.1.6
	github.com/stretchr/testify v1.9.0
	golang.org/x/crypto v0.35.0
	google.golang.org/grpc v1.59.0
	google.golang.org/protobuf v1.33.0
	nhooyr.io/websocket v1.8.7
)

require (
	github.com/Yawning/aez v0.0.0-20211027044916-e49e68abd344 // indirect
	github.com/aead/siphash v1.0.1 // indirect
	github.com/btcsuite/btcd v0.24.3-0.20250318170759-4f4ea81776d6 // indirect
	github.com/btcsuite/btcd/btcutil v1.1.5 // indirect
	github.com/btcsuite/btcd/btcutil/psbt v1.1.8 // indirect
	github.com/btcsuite/btcd/chaincfg/chainhash v1.1.0 // indirect
	github.com/btcsuite/btclog v0.0.0-20241003133417-09c4e92e319c // indirect
	github.com/btcsuite/btcwallet v0.16.13 // indirect
	github.com/btcsuite/btcwallet/wallet/txauthor v1.3.5 // indirect
	github.com/btcsuite/btcwallet/wallet/txrules v1.2.2 // indirect
	github.com/btcsuite/btcwallet/wallet/txsizes v1.2.5 // indirect
	github.com/btcsuite/btcwallet/walletdb v1.5.1 // indirect
	github.com/btcsuite/btcwallet/wtxmgr v1.5.6 // indirect
	github.com/btcsuite/go-socks v0.0.0-20170105172521-4720035b7bfd // indirect
	github.com/btcsuite/websocket v0.0.0-20150119174127-31079b680792 // indirect
	github.com/btcsuite/winsvc v1.0.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/decred/dcrd/crypto/blake256 v1.0.1 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.3.0 // indirect
	github.com/decred/dcrd/lru v1.1.2 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/jessevdk/go-flags v1.4.0 // indirect
	github.com/jrick/logrotate v1.1.2 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/lightninglabs/gozmq v0.0.0-20191113021534-d20a764486bf // indirect
	github.com/lightninglabs/neutrino v0.16.1 // indirect
	github.com/lightninglabs/neutrino/cache v1.1.2 // indirect
	github.com/lightningnetwork/lnd/clock v1.1.1 // indirect
	github.com/lightningnetwork/lnd/fn/v2 v2.0.8 // indirect
	github.com/lightningnetwork/lnd/queue v1.1.1 // indirect
	github.com/lightningnetwork/lnd/ticker v1.1.1 // indirect
	github.com/lightningnetwork/lnd/tlv v1.3.1 // indirect
	github.com/miekg/dns v1.1.43 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	github.com/syndtr/goleveldb v1.0.1-0.20210819022825-2ae1ddf74ef7 // indirect
	gitlab.com/yawning/bsaes.git v0.0.0-20190805113838-0a714cd429ec // indirect
	golang.org/x/exp v0.0.0-20240325151524-a685a6edb6d8 // indirect
	golang.org/x/net v0.25.0 // indirect
	golang.org/x/sync v0.11.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/term v0.29.0 // indirect
	golang.org/x/text v0.22.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20231016165738-49dd2c1f3d0b // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20231030173426-d783a09b4405 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/lightninglabs/lightning-node-connect/gbn => ../gbn
