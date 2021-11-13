package itest

import (
	"crypto/tls"
	"sync"

	"github.com/lightninglabs/terminal-connect/itest/mockrpc"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/terminal-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type serverHarness struct {
	serverHost string
	mockServer *grpc.Server
	server     *mockrpc.Server
	password   [8]string

	errChan chan error

	wg sync.WaitGroup
}

func newServerHarness(serverHost string) *serverHarness {
	return &serverHarness{
		serverHost: serverHost,
		errChan:    make(chan error, 1),
	}
}

func (s *serverHarness) stop() {
	s.mockServer.Stop()
	s.wg.Wait()
}

func (s *serverHarness) start() error {
	password, passwordEntropy, err := mailbox.NewPassword()
	if err != nil {
		return err
	}
	s.password = password

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return err
	}

	mailboxServer, err := mailbox.NewServer(
		s.serverHost, passwordEntropy[:], grpc.WithTransportCredentials(
			credentials.NewTLS(&tls.Config{
				InsecureSkipVerify: true,
			}),
		),
	)
	if err != nil {
		return err
	}

	ecdh := &keychain.PrivKeyECDH{PrivKey: privKey}
	noiseConn := mailbox.NewNoiseGrpcConn(ecdh, nil, passwordEntropy[:])

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
