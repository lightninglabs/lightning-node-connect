package itest

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/lightning-node-connect/itest/mockrpc"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type clientHarness struct {
	serverAddr string
	grpcClient bool
	tlsConfig  *tls.Config

	grpcConn   *grpc.ClientConn
	clientConn mockrpc.MockServiceClient
	client     *mailbox.Client

	passphraseEntropy []byte
	localStatic       keychain.SingleKeyECDH
	remoteStatic      *btcec.PublicKey

	cancel func()
}

func newClientHarness(serverAddress string, entropy []byte, insecure,
	grpcClient bool) (*clientHarness, error) {

	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{}
	if insecure {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	return &clientHarness{
		tlsConfig:         tlsConfig,
		grpcClient:        grpcClient,
		serverAddr:        serverAddress,
		passphraseEntropy: entropy,
		localStatic:       &keychain.PrivKeyECDH{PrivKey: privKey},
	}, nil
}

func (c *clientHarness) start() error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	connData := mailbox.NewConnData(
		c.localStatic, c.remoteStatic, c.passphraseEntropy,
		nil, func(key *btcec.PublicKey) error {
			c.remoteStatic = key
			return nil
		}, func(data []byte) error {
			if !bytes.Equal(data, authData) {
				return fmt.Errorf("incorrect auth data. "+
					"Expected %x, got %x", authData, data)
			}

			return nil
		},
	)

	var err error
	if c.grpcClient {
		c.client, err = mailbox.NewGrpcClient(
			ctx, c.serverAddr, connData,
			grpc.WithTransportCredentials(
				credentials.NewTLS(c.tlsConfig),
			),
		)
	} else {
		c.client, err = mailbox.NewWebsocketsClient(
			ctx, c.serverAddr, connData,
		)
	}
	if err != nil {
		return err
	}

	noiseConn := mailbox.NewNoiseGrpcConn(connData)

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(c.client.Dial),
		grpc.WithTransportCredentials(noiseConn),
		grpc.WithPerRPCCredentials(noiseConn),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(1024 * 1024 * 200),
		),
		grpc.WithBlock(),
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
