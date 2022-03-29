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

	passphraseEntropy []byte
	localStatic       keychain.SingleKeyECDH
	remoteStatic      *btcec.PublicKey

	cancel func()
}

func newClientHarness(serverAddress string, entropy []byte) (*clientHarness,
	error) {

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, err
	}

	return &clientHarness{
		serverAddr:        serverAddress,
		passphraseEntropy: entropy,
		localStatic:       &keychain.PrivKeyECDH{PrivKey: privKey},
	}, nil
}

func (c *clientHarness) start() error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	connData := mailbox.NewConnData(
		c.localStatic, c.remoteStatic, c.passphraseEntropy, nil,
		func(key *btcec.PublicKey) error {
			c.remoteStatic = key
			return nil
		}, nil,
	)

	transportConn, err := mailbox.NewClient(ctx, connData)
	if err != nil {
		return err
	}

	noiseConn := mailbox.NewNoiseGrpcConn(connData)

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
