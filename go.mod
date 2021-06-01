module github.com/lightninglabs/terminal-connect

require (
	github.com/golang/protobuf v1.4.3
	github.com/grpc-ecosystem/grpc-gateway v1.14.3
	golang.org/x/net v0.0.0-20200520004742-59133d7f0dd7 // indirect
	golang.org/x/sys v0.0.0-20210426080607-c94f62235c83 // indirect
	golang.org/x/text v0.3.3 // indirect
	google.golang.org/grpc v1.29.1
	google.golang.org/protobuf v1.23.0
)

replace git.schwanenlied.me/yawning/bsaes.git => github.com/Yawning/bsaes v0.0.0-20180720073208-c0276d75487e

// Fix incompatibility of etcd go.mod package.
// See https://github.com/etcd-io/etcd/issues/11154
replace go.etcd.io/etcd => go.etcd.io/etcd v0.5.0-alpha.5.0.20201125193152-8a03d2e9614b

replace (
	github.com/lightningnetwork/lnd => github.com/guggero/lnd v0.11.0-beta.rc4.0.20210601120905-1ba56b534c9f
	github.com/lightningnetwork/lnd/cert => github.com/guggero/lnd/cert v1.0.4-0.20210601120905-1ba56b534c9f
	github.com/lightningnetwork/lnd/healthcheck => github.com/guggero/lnd/healthcheck v0.0.0-20210601120905-1ba56b534c9f
	github.com/lightningnetwork/lnd/kvdb => github.com/guggero/lnd/kvdb v0.0.0-20210601120905-1ba56b534c9f
)

go 1.16
