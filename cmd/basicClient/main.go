package main

import (
	"context"
	"crypto/sha512"
	"crypto/tls"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
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
)

func main() {
	logWriter := build.NewRotatingLogWriter()
	interceptor, err := signal.Intercept()
	lnd.AddSubLogger(logWriter, gbn.Subsystem, interceptor, gbn.UseLogger)
	lnd.AddSubLogger(logWriter, mailbox.Subsystem, interceptor, mailbox.UseLogger)
	logWriter.SetLogLevels("debug")

	words := os.Args[1:9]

	client, err := lndConn(words)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		client.Close()
		time.Sleep(time.Second)
	}()

	go func() {
		s := <-interceptor.ShutdownChannel()
		log.Printf("got signal %v, attempting graceful shutdown", s)
		client.Close()
	}()

	c := mockrpc.NewMockServiceClient(client)

	if err := chatWithLND(c); err != nil {
		log.Fatal(err)
	}
}

func chatWithLND(c mockrpc.MockServiceClient) error {

	largeResp := make([]byte, 1024*1024*4)
	rand.Read(largeResp)
	req := &mockrpc.Request{Req: largeResp}

	for i := 0; i < 3; i++ {
		t := time.Now()
		_, err := c.MockServiceMethod(context.Background(), req)
		if err != nil {
			return err
		}

		fmt.Println("got the thing", time.Since(t))
		time.Sleep(5 * time.Second)
	}

	return nil
}

func lndConn(words []string) (*grpc.ClientConn, error) {
	var mnemonicWords [mailbox.NumPasswordWords]string
	copy(mnemonicWords[:], words)
	password := mailbox.PasswordMnemonicToEntropy(mnemonicWords)

	sid := sha512.Sum512(password[:])
	receiveSID := mailbox.GetSID(sid, true)
	sendSID := mailbox.GetSID(sid, false)

	privKey, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, err
	}
	ecdh := &keychain.PrivKeyECDH{PrivKey: privKey}

	ctx := context.Background()
	transportConn := mailbox.NewClientConn(ctx, receiveSID, sendSID)
	noiseConn := mailbox.NewNoiseConn(ecdh, nil)

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(transportConn.Dial),
		grpc.WithTransportCredentials(noiseConn),
		grpc.WithPerRPCCredentials(noiseConn),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(1024 * 1024 * 200),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                15 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}

	return grpc.DialContext(ctx, "localhost:11110", dialOpts...)
}
