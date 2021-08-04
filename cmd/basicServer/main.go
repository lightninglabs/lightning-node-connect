package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/keepalive"

	"github.com/lightninglabs/terminal-connect/gbn"

	"github.com/lightningnetwork/lnd"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"

	"github.com/lightninglabs/terminal-connect/itest/mockrpc"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/terminal-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	logWriter := build.NewRotatingLogWriter()
	interceptor, _ := signal.Intercept()
	lnd.AddSubLogger(logWriter, gbn.Subsystem, interceptor, gbn.UseLogger)
	lnd.AddSubLogger(logWriter, mailbox.Subsystem, interceptor, mailbox.UseLogger)
	logWriter.SetLogLevels("debug")

	// Create a new single-use password.
	password, passwordEntropy, err := mailbox.NewPassword()
	if err != nil {
		log.Fatalf(err.Error())
	}

	fmt.Println(strings.Join(password[:], " "))

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		log.Fatalf(err.Error())
	}

	// mailboxServer implements net.Listener
	mailboxServer, err := mailbox.NewServer(
		"127.0.0.1:11110",
		passwordEntropy[:],
		grpc.WithTransportCredentials(
			credentials.NewTLS(&tls.Config{
				InsecureSkipVerify: true,
			}),
		),
	)
	if err != nil {
		log.Fatalf(err.Error())
	}

	ecdh := &keychain.PrivKeyECDH{PrivKey: privKey}
	noiseConn := mailbox.NewNoiseConn(ecdh, nil)

	s := &mockrpc.Server{}
	largeResp := make([]byte, 1024*1024*4)
	rand.Read(largeResp)

	grpcServer := grpc.NewServer(
		grpc.Creds(noiseConn),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    time.Second * 10,
			Timeout: time.Second * 5,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	mockrpc.RegisterMockServiceServer(grpcServer, s)

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		s := <-interceptor.ShutdownChannel()
		log.Printf("got signal %v, attempting graceful shutdown", s)
		grpcServer.GracefulStop()
		wg.Done()
	}()

	fmt.Printf("Mock RPC server listening on %s", mailboxServer.Addr())

	log.Println("starting grpc server")
	err = grpcServer.Serve(mailboxServer)
	if err != nil {
		log.Fatalf("could not serve: %v", err)
	}
	wg.Wait()
	log.Println("clean shutdown")
}
