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
	insecure   bool
	mockServer *grpc.Server
	server     *mockrpc.Server
	password   [mailbox.NumPasswordWords]string
	localKey   keychain.SingleKeyECDH
	remoteKey  *btcec.PublicKey

	errChan chan error

	wg sync.WaitGroup
}

func newServerHarness(serverHost string, insecure bool) (*serverHarness, error) {
	password, _, err := mailbox.NewPassword()
	if err != nil {
		return nil, err
	}

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, err
	}

	return &serverHarness{
		serverHost: serverHost,
		insecure:   insecure,
		password:   password,
		localKey:   &keychain.PrivKeyECDH{PrivKey: privKey},
		errChan:    make(chan error, 1),
	}, nil
}

func (s *serverHarness) stop() {
	s.mockServer.Stop()
	s.wg.Wait()
}

func (s *serverHarness) start() error {
	tlsConfig := &tls.Config{}
	if s.insecure {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	pswdEntropy := mailbox.PasswordMnemonicToEntropy(s.password)
	noiseConn := mailbox.NewNoiseGrpcConn(
		s.localKey, s.remoteKey, nil, pswdEntropy[:],
		func(remoteKey *btcec.PublicKey) {
			s.remoteKey = remoteKey
		},
	)

	sid, err := noiseConn.SID()
	if err != nil {
		return err
	}

	mailboxServer, err := mailbox.NewServer(
		s.serverHost, sid, grpc.WithTransportCredentials(
			credentials.NewTLS(tlsConfig),
		),
	)
	if err != nil {
		return err
	}

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
