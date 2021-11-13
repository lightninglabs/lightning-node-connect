package itest

import (
	"context"
	"crypto/sha512"
	"crypto/tls"
	"net/http"

	"github.com/lightninglabs/terminal-connect/itest/mockrpc"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/terminal-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
)

type clientHarness struct {
	serverAddr string

	grpcConn   *grpc.ClientConn
	clientConn mockrpc.MockServiceClient
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

	sid := sha512.Sum512(password[:])
	receiveSID := mailbox.GetSID(sid, true)
	sendSID := mailbox.GetSID(sid, false)

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return err
	}
	ecdh := &keychain.PrivKeyECDH{PrivKey: privKey}

	ctx := context.Background()
	transportConn := mailbox.NewClientConn(ctx, receiveSID, sendSID)
	noiseConn := mailbox.NewNoiseGrpcConn(ecdh, nil, password[:])

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
	return c.grpcConn.Close()
}
