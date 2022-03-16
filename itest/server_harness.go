package itest

import (
	"crypto/tls"
	"sync"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/lightning-node-connect/itest/mockrpc"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type serverHarness struct {
	serverHost string
	mockServer *grpc.Server
	server     *mockrpc.Server

	password     []byte
	localStatic  keychain.SingleKeyECDH
	remoteStatic *btcec.PublicKey

	tlsConfig *tls.Config

	errChan chan error
	wg      sync.WaitGroup
}

func newServerHarness(serverHost string, insecure bool) (*serverHarness,
	error) {

	password, _, err := mailbox.NewPassword()
	if err != nil {
		return nil, err
	}
	pswdEntropy := mailbox.PasswordMnemonicToEntropy(password)

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{}
	if insecure {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	return &serverHarness{
		serverHost:  serverHost,
		password:    pswdEntropy[:],
		localStatic: &keychain.PrivKeyECDH{PrivKey: privKey},
		tlsConfig:   tlsConfig,
		errChan:     make(chan error, 1),
	}, nil
}

func (s *serverHarness) stop() {
	s.mockServer.Stop()
	s.wg.Wait()
}

func (s *serverHarness) start() error {
	mailboxServer, err := mailbox.NewServer(
		s.serverHost, s.password[:], grpc.WithTransportCredentials(
			credentials.NewTLS(s.tlsConfig),
		),
	)
	if err != nil {
		return err
	}

	noiseConn := mailbox.NewNoiseGrpcConn(s.localStatic, nil, s.password[:])

	s.mockServer = grpc.NewServer(
		grpc.Creds(noiseConn),
		grpc.MaxRecvMsgSize(1024*1024*200),
	)
	s.server = &mockrpc.Server{}

	mockrpc.RegisterMockServiceServer(s.mockServer, s.server)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.errChan <- s.mockServer.Serve(mailboxServer)
	}()

	return nil
}
