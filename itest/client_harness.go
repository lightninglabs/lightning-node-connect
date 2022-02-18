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
	password   [14]byte

	localStaticKey  *keychain.PrivKeyECDH
	remoteStaticKey *btcec.PublicKey

	grpcConn   *grpc.ClientConn
	clientConn mockrpc.MockServiceClient

	cancel func()
}

func newClientHarness(serverAddress string, words [10]string) (*clientHarness,
	error) {

	password := mailbox.PasswordMnemonicToEntropy(words)

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, err
	}

	return &clientHarness{
		serverAddr:     serverAddress,
		password:       password,
		localStaticKey: &keychain.PrivKeyECDH{PrivKey: privKey},
	}, nil
}

func (c *clientHarness) start() error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	noiseConn := mailbox.NewNoiseGrpcConn(
		c.localStaticKey, c.remoteStaticKey, nil, c.password[:],
		func(remoteKey *btcec.PublicKey) {
			c.remoteStaticKey = remoteKey
		},
	)

	sid, err := noiseConn.SID()
	if err != nil {
		return err
	}

	transportConn, err := mailbox.NewClient(ctx, c.serverAddr, sid)
	if err != nil {
		return err
	}

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(transportConn.Dial),
		grpc.WithTransportCredentials(noiseConn),
		grpc.WithPerRPCCredentials(noiseConn),
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
