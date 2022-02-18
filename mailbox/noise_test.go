package mailbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/stretchr/testify/require"
)

// TestSpake2Mask tests the masking operation for SPAK2 to ensure that ti's
// properly reverseable.
func TestSpake2Mask(t *testing.T) {
	t.Parallel()

	priv, err := btcec.NewPrivateKey(btcec.S256())
	require.NoError(t, err)

	pub := priv.PubKey()

	pass := []byte("top secret")
	passHash := sha256.Sum256(pass)

	maskedPoint := ekeMask(pub, passHash[:])
	require.True(t, !maskedPoint.IsEqual(pub))

	unmaskedPoint := ekeUnmask(maskedPoint, passHash[:])
	require.True(t, unmaskedPoint.IsEqual(pub))
}

// TestXXHandshake tests that a client and server can successfully complete a
// Noise_XX pattern handshake and then use the encrypted connection to exchange
// messages afterwards.
func TestXXHandshake(t *testing.T) {
	// First, generate static keys for each party.
	pk1, err := btcec.NewPrivateKey(btcec.S256())
	require.NoError(t, err)

	pk2, err := btcec.NewPrivateKey(btcec.S256())
	require.NoError(t, err)

	// Create a password that will be used to mask the first ephemeral key.
	pass := []byte("top secret")
	passHash := sha256.Sum256(pass)

	// The server will be initialised with auth data that it is expected to
	// send to the client during act 2 of the handshake.
	authData := []byte("authData")

	// Create a pipe and give one end to the client and one to the server
	// as the underlying transport.
	conn1, conn2 := newMockProxyConns()
	defer func() {
		conn1.Close()
		conn2.Close()
	}()

	// Create a server.
	server := NewNoiseGrpcConn(
		&keychain.PrivKeyECDH{PrivKey: pk1}, nil, authData, passHash[:],
		func(remoteKey *btcec.PublicKey) {},
	)

	// Spin off the server's handshake process.
	var (
		serverConn    net.Conn
		serverErrChan = make(chan error)
	)
	go func() {
		var err error
		serverConn, _, err = server.ServerHandshake(conn1)
		serverErrChan <- err
	}()

	// Create a client.
	client := NewNoiseGrpcConn(
		&keychain.PrivKeyECDH{PrivKey: pk2}, nil, nil, passHash[:],
		func(remoteKey *btcec.PublicKey) {},
	)

	// Start the client's handshake process.
	clientConn, _, err := client.ClientHandshake(
		context.Background(), "", conn2,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the server's handshake to complete or timeout.
	select {
	case err := <-serverErrChan:
		if err != nil {
			t.Fatal(err)
		}

	case <-time.After(time.Second):
		t.Fatalf("handshake timeout")
	}

	// Ensure that any auth data was successfully received by the client.
	require.True(t, bytes.Equal(client.authData, authData))

	// Also check that both parties now have the other parties static key.
	require.True(t, client.remoteKey.IsEqual(pk1.PubKey()))
	require.True(t, server.remoteKey.IsEqual(pk2.PubKey()))

	// Check that messages can be sent between client and server normally
	// now.
	msg := make(chan []byte, 10)
	quit := make(chan struct{})
	go func() {
		var payload []byte
		for {
			select {
			case payload = <-msg:

			case <-quit:
				return
			}
			_, err := clientConn.Write(payload)
			require.NoError(t, err)
		}
	}()

	for i := 0; i < 10; i++ {
		testMessage := []byte(fmt.Sprintf("test message %d", i))

		msg <- testMessage

		recvBuffer := make([]byte, len(testMessage))
		_, err = serverConn.Read(recvBuffer)
		require.NoError(t, err)
		require.True(t, bytes.Equal(recvBuffer, testMessage))
	}

	close(quit)
}

// TestKKHandshake tests that a client and server Machine can successfully
// complete a Noise_KK pattern handshake.
func TestKKHandshake(t *testing.T) {
	// First, generate static keys for each party.
	pk1, err := btcec.NewPrivateKey(btcec.S256())
	require.NoError(t, err)

	pk2, err := btcec.NewPrivateKey(btcec.S256())
	require.NoError(t, err)

	// Create a password that will be used to mask the first ephemeral key.
	pass := []byte("top secret")
	passHash := sha256.Sum256(pass)

	// The server will be initialised with auth data that it is expected to
	// send to the client during act 2 of the handshake.
	authData := []byte("authData")

	// Create a pipe and give one end to the client and one to the server
	// as the underlying transport.
	conn1, conn2 := newMockProxyConns()
	defer func() {
		conn1.Close()
		conn2.Close()
	}()

	// First, we'll initialize a new state machine for the server with our
	// static key, remote static key, passphrase, and also the
	// authentication data.
	server, err := NewBrontideMachine(&BrontideMachineConfig{
		Initiator:           false,
		HandshakePattern:    KKPattern,
		MinHandshakeVersion: MinHandshakeVersion,
		MaxHandshakeVersion: MaxHandshakeVersion,
		LocalStaticKey:      &keychain.PrivKeyECDH{PrivKey: pk1},
		RemoteStaticKey:     pk2.PubKey(),
		PAKEPassphrase:      passHash[:],
		AuthData:            authData,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Spin off the server's handshake process.
	var serverErrChan = make(chan error)
	go func() {
		err := server.DoHandshake(conn1)
		serverErrChan <- err
	}()

	// Create a client.
	client, err := NewBrontideMachine(&BrontideMachineConfig{
		Initiator:           true,
		HandshakePattern:    KKPattern,
		MinHandshakeVersion: MinHandshakeVersion,
		MaxHandshakeVersion: MaxHandshakeVersion,
		LocalStaticKey:      &keychain.PrivKeyECDH{PrivKey: pk2},
		RemoteStaticKey:     pk1.PubKey(),
		PAKEPassphrase:      passHash[:],
	})
	if err != nil {
		t.Fatal(err)
	}

	// Start the client's handshake process.
	if err := client.DoHandshake(conn2); err != nil {
		t.Fatal(err)
	}

	// Wait for the server's handshake to complete or timeout.
	select {
	case err := <-serverErrChan:
		if err != nil {
			t.Fatal(err)
		}

	case <-time.After(time.Second):
		t.Fatalf("handshake timeout")
	}

	// Ensure that any auth data was successfully received by the client.
	require.True(t, bytes.Equal(client.receivedPayload, authData))

	// Also check that both parties now have the other parties static key.
	require.True(t, client.remoteStatic.IsEqual(pk1.PubKey()))
	require.True(t, server.remoteStatic.IsEqual(pk2.PubKey()))

	// Check that messages can be sent between client and server normally
	// now.
	msg := make(chan []byte, 10)
	quit := make(chan struct{})
	go func() {
		clientConn := &NoiseGrpcConn{
			noise:      client,
			SwitchConn: conn2,
		}
		var payload []byte
		for {
			select {
			case payload = <-msg:

			case <-quit:
				return
			}
			_, err := clientConn.Write(payload)
			require.NoError(t, err)
		}
	}()

	serverConn := &NoiseGrpcConn{
		noise:      server,
		SwitchConn: conn1,
	}
	for i := 0; i < 10; i++ {
		testMessage := []byte(fmt.Sprintf("test message %d", i))

		msg <- testMessage

		recvBuffer := make([]byte, len(testMessage))
		_, err = serverConn.Read(recvBuffer)
		require.NoError(t, err)
		require.True(t, bytes.Equal(recvBuffer, testMessage))
	}
}

// TestHandshake tests that client and server are able successfully perform
// a handshake.
func TestHandshake(t *testing.T) {
	largeAuthData := make([]byte, 3*1024*1024)
	_, err := rand.Read(largeAuthData)
	require.NoError(t, err)

	tests := []struct {
		name             string
		serverMinVersion byte
		serverMaxVersion byte
		clientMinVersion byte
		clientMaxVersion byte
		expectedVersion  byte
		authData         []byte
	}{
		{
			name:             "server v0 and client v0",
			serverMinVersion: HandshakeVersion0,
			serverMaxVersion: HandshakeVersion0,
			clientMinVersion: HandshakeVersion0,
			clientMaxVersion: HandshakeVersion0,
			expectedVersion:  HandshakeVersion0,
			authData:         []byte{0, 1, 2, 3},
		},
		{
			name:             "server v1 and client v1",
			serverMinVersion: HandshakeVersion1,
			serverMaxVersion: HandshakeVersion1,
			clientMinVersion: HandshakeVersion1,
			clientMaxVersion: HandshakeVersion1,
			expectedVersion:  HandshakeVersion1,
			authData:         largeAuthData,
		},
		{
			name:             "server v0 and client [v0, v1]",
			serverMinVersion: HandshakeVersion0,
			serverMaxVersion: HandshakeVersion0,
			clientMinVersion: HandshakeVersion0,
			clientMaxVersion: HandshakeVersion1,
			expectedVersion:  HandshakeVersion0,
			authData:         []byte{0, 1, 2, 3},
		},
		{
			name:             "server v1 and client [v0, v1]",
			serverMinVersion: HandshakeVersion0,
			serverMaxVersion: HandshakeVersion1,
			clientMinVersion: HandshakeVersion0,
			clientMaxVersion: HandshakeVersion1,
			expectedVersion:  HandshakeVersion1,
			authData:         largeAuthData,
		},
		{
			name:             "server v2 and client [v0, v2]",
			serverMinVersion: HandshakeVersion0,
			serverMaxVersion: HandshakeVersion2,
			clientMinVersion: HandshakeVersion0,
			clientMaxVersion: HandshakeVersion2,
			expectedVersion:  HandshakeVersion2,
			authData:         []byte{0, 1, 2, 3},
		},
		{
			name:             "server v1 and client [v0, v2]",
			serverMinVersion: HandshakeVersion0,
			serverMaxVersion: HandshakeVersion1,
			clientMinVersion: HandshakeVersion0,
			clientMaxVersion: HandshakeVersion2,
			expectedVersion:  HandshakeVersion1,
			authData:         []byte{0, 1, 2, 3},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			// First, generate static keys for each party.
			pk1, err := btcec.NewPrivateKey(btcec.S256())
			require.NoError(t, err)

			pk2, err := btcec.NewPrivateKey(btcec.S256())
			require.NoError(t, err)

			conn1, conn2 := newMockProxyConns()
			defer func() {
				conn1.Close()
				conn2.Close()
			}()

			server := NewNoiseGrpcConn(
				&keychain.PrivKeyECDH{PrivKey: pk1}, nil,
				test.authData, pass,
				func(remoteKey *btcec.PublicKey) {},
				WithMinHandshakeVersion(test.serverMinVersion),
				WithMaxHandshakeVersion(test.serverMaxVersion),
			)

			client := NewNoiseGrpcConn(
				&keychain.PrivKeyECDH{PrivKey: pk2}, nil, nil,
				pass, func(remoteKey *btcec.PublicKey) {},
				WithMinHandshakeVersion(test.clientMinVersion),
				WithMaxHandshakeVersion(test.clientMaxVersion),
			)

			var (
				serverConn net.Conn
			)
			serverErrChan := make(chan error)
			go func() {
				var err error
				serverConn, _, err = server.ServerHandshake(
					conn1,
				)
				serverErrChan <- err
			}()

			clientConn, _, err := client.ClientHandshake(
				context.Background(), "", conn2,
			)
			if err != nil {
				t.Fatal(err)
			}

			select {
			case err := <-serverErrChan:
				if err != nil {
					t.Fatal(err)
				}

			case <-time.After(time.Second):
				t.Fatalf("handshake timeout")
			}

			// Ensure that any auth data was successfully received
			// by the client.
			require.True(
				t, bytes.Equal(client.authData, test.authData),
			)

			// Ensure that the negotiated version is as expected.
			require.Equal(
				t, test.expectedVersion, client.noise.version,
			)
			require.Equal(
				t, test.expectedVersion, server.noise.version,
			)

			// For handshake versions above v2, we expect each peer
			// to have stored the others static key. Otherwise,
			// we expect the remote key to be nil.
			if test.expectedVersion >= HandshakeVersion2 {
				require.True(t, client.remoteKey.IsEqual(server.localKey.PubKey()))
				require.True(t, server.remoteKey.IsEqual(client.localKey.PubKey()))
			} else {
				require.Nil(t, client.remoteKey)
				require.Nil(t, server.remoteKey)
			}

			// Check that messages can be sent between client and
			// server normally now.
			testMessage := []byte("test message")
			go func() {
				_, err := clientConn.Write(testMessage)
				require.NoError(t, err)
			}()

			recvBuffer := make([]byte, len(testMessage))
			_, err = serverConn.Read(recvBuffer)
			require.NoError(t, err)
			require.True(t, bytes.Equal(recvBuffer, testMessage))
		})
	}
}

var _ ProxyConn = (*mockProxyConn)(nil)

type mockProxyConn struct {
	net.Conn
}

func (m *mockProxyConn) SetRecvTimeout(_ time.Duration) {}

func (m *mockProxyConn) SetSendTimeout(_ time.Duration) {}

func (m *mockProxyConn) ReceiveControlMsg(_ ControlMsg) error {
	return nil
}

func (m *mockProxyConn) SendControlMsg(_ ControlMsg) error {
	return nil
}

func newMockProxyConns() (*SwitchConn, *SwitchConn) {
	c1, c2 := net.Pipe()
	c3, c4 := net.Pipe()

	var switchCount1, switchCount2 int

	switchConn1, _ := newSwitchConn(&SwitchConfig{
		NewProxyConn: func(sid [64]byte) (ProxyConn, error) {
			if switchCount1 == 0 {
				switchCount1++
				return &mockProxyConn{c1}, nil
			}

			return &mockProxyConn{c3}, nil
		},
		StopProxyConn: func(conn ProxyConn) error {
			return nil
		},
	})

	switchConn2, _ := newSwitchConn(&SwitchConfig{
		NewProxyConn: func(sid [64]byte) (ProxyConn, error) {
			if switchCount2 == 0 {
				switchCount2++
				return &mockProxyConn{c2}, nil
			}
			return &mockProxyConn{c4}, nil
		},
		StopProxyConn: func(conn ProxyConn) error {
			return nil
		},
	})
	return switchConn1, switchConn2
}
