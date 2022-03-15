package itest

import (
	"context"
	"crypto/tls"
	"net/http"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/lightning-node-connect/itest/mockrpc"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
)

type clientHarness struct {
	serverAddr string

	localStatic  keychain.SingleKeyECDH
	remoteStatic *btcec.PublicKey

	password []byte

	grpcConn   *grpc.ClientConn
	clientConn mockrpc.MockServiceClient

	cancel func()
}

func newClientHarness(serverAddress string, words [10]string) (*clientHarness,
	error) {

	var mnemonicWords [mailbox.NumPasswordWords]string
	copy(mnemonicWords[:], words[:])
	password := mailbox.PasswordMnemonicToEntropy(mnemonicWords)

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, err
	}
	ecdh := &keychain.PrivKeyECDH{PrivKey: privKey}

	return &clientHarness{
		localStatic: ecdh,
		serverAddr:  serverAddress,
		password:    password[:],
	}, nil
}

func (c *clientHarness) start() error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	clientConn, err := mailbox.NewClient(
		ctx, c.serverAddr, c.localStatic, c.remoteStatic, c.password,
		func(key *btcec.PublicKey) {
			c.remoteStatic = key
		},
	)
	if err != nil {
		return err
	}

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(clientConn.Dial),
		grpc.WithTransportCredentials(&mailbox.FakeCredentials{}),
		grpc.WithPerRPCCredentials(clientConn),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(1024 * 1024 * 200),
		),
	}

	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}

	client, err := grpc.DialContext(ctx, "", dialOpts...)
	if err != nil {
		return err
	}

	c.grpcConn = client
	c.clientConn = mockrpc.NewMockServiceClient(client)

	return nil
}

func (c *clientHarness) cleanup() error {
	c.cancel()
	return c.grpcConn.Close()
}
