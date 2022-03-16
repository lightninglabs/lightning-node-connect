//go:build js
// +build js

package main

import (
	"context"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
)

func mailboxRPCConnection(mailboxServer,
	pairingPhrase string) (*grpc.ClientConn, error) {

	words := strings.Split(pairingPhrase, " ")
	var mnemonicWords [mailbox.NumPasswordWords]string
	copy(mnemonicWords[:], words)
	password := mailbox.PasswordMnemonicToEntropy(mnemonicWords)

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, err
	}
	ecdh := &keychain.PrivKeyECDH{PrivKey: privKey}

	ctx := context.Background()
	transportConn, err := mailbox.NewClient(ctx, ecdh, nil, password[:])
	if err != nil {
		return nil, err
	}

	noiseConn := mailbox.NewNoiseGrpcConn(ecdh, nil, password[:])

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(transportConn.Dial),
		grpc.WithTransportCredentials(noiseConn),
		grpc.WithPerRPCCredentials(noiseConn),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(1024 * 1024 * 200),
		),
	}

	return grpc.DialContext(ctx, mailboxServer, dialOpts...)
}
