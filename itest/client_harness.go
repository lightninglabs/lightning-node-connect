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

	grpcConn   *grpc.ClientConn
	clientConn mockrpc.MockServiceClient

	cancel func()
}

func newClientHarness(serverAddress string) *clientHarness {
	return &clientHarness{
		serverAddr: serverAddress,
	}
}

func (c *clientHarness) setConn(words []string) error {
	var mnemonicWords [mailbox.NumPasswordWords]string
	copy(mnemonicWords[:], words)
	password := mailbox.PasswordMnemonicToEntropy(mnemonicWords)

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return err
	}
	ecdh := &keychain.PrivKeyECDH{PrivKey: privKey}

	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	clientConn, err := mailbox.NewClient(ctx, ecdh, password[:])
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

	client, err := grpc.DialContext(ctx, c.serverAddr, dialOpts...)
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
