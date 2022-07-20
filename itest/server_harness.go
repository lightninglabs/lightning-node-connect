package itest

import (
	"crypto/tls"
	"sync"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/lightning-node-connect/itest/mockrpc"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var authData = []byte{1, 2, 3, 4}

type serverHarness struct {
	serverHost string
	mockServer *grpc.Server
	server     *mockrpc.Server

	passphraseEntropy []byte
	localStatic       keychain.SingleKeyECDH
	remoteStatic      *btcec.PublicKey

	tlsConfig *tls.Config

	errChan chan error
	wg      sync.WaitGroup
}

func newServerHarness(serverHost string, insecure bool) (*serverHarness,
	error) {

	entropy, _, err := mailbox.NewPassphraseEntropy()
	if err != nil {
		return nil, err
	}
	pswdEntropy := mailbox.PassphraseMnemonicToEntropy(entropy)

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

	return &serverHarness{
		serverHost:        serverHost,
		passphraseEntropy: pswdEntropy[:],
		localStatic:       &keychain.PrivKeyECDH{PrivKey: privKey},
		tlsConfig:         tlsConfig,
		errChan:           make(chan error, 1),
	}, nil
}

func (s *serverHarness) stop() {
	s.mockServer.Stop()
	s.wg.Wait()
}

func (s *serverHarness) start() error {
	connData := mailbox.NewConnData(
		s.localStatic, s.remoteStatic, s.passphraseEntropy, authData,
		func(key *btcec.PublicKey) error {
			s.remoteStatic = key
			return nil
		}, nil,
	)

	mailboxServer, err := mailbox.NewServer(
		s.serverHost, connData, grpc.WithTransportCredentials(
			credentials.NewTLS(s.tlsConfig),
		),
	)
	if err != nil {
		return err
	}

	noiseConn := mailbox.NewNoiseGrpcConn(connData)

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
