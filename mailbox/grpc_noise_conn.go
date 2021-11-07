package mailbox

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc/credentials"
)

var _ credentials.TransportCredentials = (*NoiseConn)(nil)
var _ credentials.PerRPCCredentials = (*NoiseConn)(nil)

const (
	defaultGrpcWriteBufSize = 32 * 1024
)

// NoiseConn is a type that implements the credentials.TransportCredentials
// interface and can therefore be used as a replacement of the default TLS
// implementation that's used by HTTP/2.
type NoiseConn struct {
	// TODO(roasbee): eliminate this so can be used as generic handshake
	// bridge?
	MailboxConn

	password []byte
	authData []byte

	localKey  keychain.SingleKeyECDH
	remoteKey *btcec.PublicKey

	nextMsg    []byte
	nextMsgMtx sync.Mutex

	noise *Machine
}

// NewNoiseConn creates a new noise connection using given local ECDH key. The
// auth data can be set for server connections and is sent as the payload to the
// client during the handshake.
func NewNoiseConn(localKey keychain.SingleKeyECDH,
	authData []byte, password []byte) *NoiseConn {

	return &NoiseConn{
		localKey: localKey,
		authData: authData,
		password: password,
	}
}

// Read tries to read an encrypted data message from the underlying control
// connection and then tries to decrypt it.
//
// NOTE: This is part of the net.Conn interface.
func (c *NoiseConn) Read(b []byte) (n int, err error) {
	c.nextMsgMtx.Lock()
	defer c.nextMsgMtx.Unlock()

	// The last read was incomplete, return the few bytes that didn't fit.
	if len(c.nextMsg) > 0 {
		msgLen := len(c.nextMsg)
		copy(b, c.nextMsg)

		c.nextMsg = nil
		return msgLen, nil
	}

	requestBytes, err := c.noise.ReadMessage(c.MailboxConn)
	if err != nil {
		return 0, fmt.Errorf("error decrypting payload: %v", err)
	}

	// Do we need to read this message in two parts? We cannot give the
	// gRPC layer above us more than the default read buffer size of 32k
	// bytes at a time.
	if len(requestBytes) > defaultGrpcWriteBufSize {
		nextMsgLen := len(requestBytes) - defaultGrpcWriteBufSize
		c.nextMsg = make([]byte, nextMsgLen)

		copy(c.nextMsg[0:nextMsgLen], requestBytes[defaultGrpcWriteBufSize:])

		copy(b, requestBytes[0:defaultGrpcWriteBufSize])
		return defaultGrpcWriteBufSize, nil
	}

	copy(b, requestBytes)
	return len(requestBytes), nil
}

// Write encrypts the given application level payload and sends it as a data
// message over the underlying control connection.
//
// NOTE: This is part of the net.Conn interface.
func (c *NoiseConn) Write(b []byte) (int, error) {
	err := c.noise.WriteMessage(b)
	if err != nil {
		return 0, err
	}

	return c.noise.Flush(c.MailboxConn)
}

// LocalAddr returns the local address of this connection.
//
// NOTE: This is part of the Conn interface.
func (c *NoiseConn) LocalAddr() net.Addr {
	if c.MailboxConn == nil {
		return &NoiseAddr{PubKey: c.localKey.PubKey()}
	}

	return &NoiseAddr{
		PubKey: c.localKey.PubKey(),
		Server: c.MailboxConn.LocalAddr().String(),
	}
}

// RemoteAddr returns the remote address of this connection.
//
// NOTE: This is part of the Conn interface.
func (c *NoiseConn) RemoteAddr() net.Addr {
	if c.MailboxConn == nil {
		return &NoiseAddr{PubKey: c.remoteKey}
	}

	return &NoiseAddr{
		PubKey: c.remoteKey,
		Server: c.MailboxConn.RemoteAddr().String(),
	}
}

// ClientHandshake implements the client side part of the noise connection
// handshake.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseConn) ClientHandshake(_ context.Context, _ string,
	conn net.Conn) (net.Conn, credentials.AuthInfo, error) {

	log.Tracef("Starting client handshake")

	transportConn, ok := conn.(MailboxConn)
	if !ok {
		return nil, nil, fmt.Errorf("invalid connection type")
	}
	c.MailboxConn = transportConn

	// First, initialize a new noise machine with our static long term, and
	// password.
	//
	// TODO(roasbeef): use memory hard function here after testing in
	// browser to ensure isn't too perf intensive
	c.noise = NewBrontideMachine(true, c.localKey, c.password)

	log.Debugf("Kicking off client handshake with client_key=%x",
		c.localKey.PubKey().SerializeCompressed())

	// Initiate the handshake by sending the first act to the receiver.
	actOne, err := c.noise.GenActOne()
	if err != nil {
		return nil, nil, err
	}
	if _, err := c.MailboxConn.Write(actOne[:]); err != nil {
		return nil, nil, err
	}

	// We'll ensure that we get ActTwo from the remote peer in a timely
	// manner. If they don't respond within 1s, then we'll kill the
	// connection.
	err = c.MailboxConn.SetReadDeadline(time.Now().Add(handshakeReadTimeout))
	if err != nil {
		return nil, nil, err
	}

	// If the first act was successful (we know that address is actually
	// remotePub), then read the second act after which we'll be able to
	// send our static public key to the remote peer with strong forward
	// secrecy.
	var actTwo [ActTwoSize]byte
	if _, err := io.ReadFull(c.MailboxConn, actTwo[:]); err != nil {
		return nil, nil, err
	}

	if err := c.noise.RecvActTwo(actTwo); err != nil {
		return nil, nil, err
	}

	// Finally, complete the handshake by sending over our encrypted static
	// key and execute the final ECDH operation.
	actThree, err := c.noise.GenActThree()
	if err != nil {
		return nil, nil, err
	}
	if _, err := c.MailboxConn.Write(actThree[:]); err != nil {
		return nil, nil, err
	}

	// We'll reset the deadline as it's no longer critical beyond the
	// initial handshake.
	err = c.MailboxConn.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, nil, err
	}

	log.Debugf("Completed client handshake with with server_key=%x",
		c.noise.remoteStatic.SerializeCompressed())

	// At this point, we'll also extract the auth data obtained during the
	// second act of the handshake.
	c.authData = c.noise.authData

	log.Tracef("Client handshake completed")

	return c, NewAuthInfo(), nil
}

// ServerHandshake implements the server part of the noise connection handshake.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseConn) ServerHandshake(conn net.Conn) (net.Conn,
	credentials.AuthInfo, error) {

	log.Tracef("Starting server handshake")

	transportConn, ok := conn.(MailboxConn)
	if !ok {
		return nil, nil, fmt.Errorf("invalid connection type")
	}
	c.MailboxConn = transportConn

	// First, we'll initialize a new borntide machine with our static key,
	// passphrase, and also the macaroon authentication data.
	c.noise = NewBrontideMachine(
		false, c.localKey, c.password, AuthDataPayload(c.authData),
	)

	// We'll ensure that we get ActOne from the remote peer in a timely
	// manner. If they don't respond within 1s, then we'll kill the
	// connection.
	err := c.MailboxConn.SetReadDeadline(time.Now().Add(handshakeReadTimeout))
	if err != nil {
		return nil, nil, err
	}

	// Attempt to carry out the first act of the handshake protocol. If the
	// connecting node doesn't know our long-term static public key, then
	// this portion will fail with a non-nil error.
	var actOne [ActOneSize]byte
	if _, err := io.ReadFull(conn, actOne[:]); err != nil {
		return nil, nil, err
	}
	if err := c.noise.RecvActOne(actOne); err != nil {
		return nil, nil, err
	}

	// Next, progress the handshake processes by sending over our ephemeral
	// key for the session along with an authenticating tag.
	actTwo, err := c.noise.GenActTwo()
	if err != nil {
		return nil, nil, err
	}
	if _, err := conn.Write(actTwo[:]); err != nil {
		return nil, nil, err
	}

	// We'll ensure that we get ActTwo from the remote peer in a timely
	// manner. If they don't respond within 1 second, then we'll kill the
	// connection.
	err = conn.SetReadDeadline(time.Now().Add(handshakeReadTimeout))
	if err != nil {
		return nil, nil, err
	}

	// Finally, finish the handshake processes by reading and decrypting
	// the connection peer's static public key. If this succeeds then both
	// sides have mutually authenticated each other.
	var actThree [ActThreeSize]byte
	if _, err := io.ReadFull(conn, actThree[:]); err != nil {
		return nil, nil, err
	}
	if err := c.noise.RecvActThree(actThree); err != nil {
		return nil, nil, err
	}

	// We'll reset the deadline as it's no longer critical beyond the
	// initial handshake.
	err = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, nil, err
	}

	log.Debugf("Finished server handshake, client_key=%x",
		c.noise.remoteStatic.SerializeCompressed())

	return c, NewAuthInfo(), nil
}

// Info returns general information about the protocol that's being used for
// this connection.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseConn) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{
		ProtocolVersion:  fmt.Sprintf("%d", ProtocolVersion),
		SecurityProtocol: ProtocolName,
		ServerName:       "lnd",
	}
}

// Clone makes a copy of this TransportCredentials.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseConn) Clone() credentials.TransportCredentials {
	return &NoiseConn{
		MailboxConn: c.MailboxConn,
		authData:    c.authData,
		localKey:    c.localKey,
		remoteKey:   c.remoteKey,
	}
}

// OverrideServerName overrides the server name used to verify the hostname on
// the returned certificates from the server.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseConn) OverrideServerName(_ string) error {
	return nil
}

// RequireTransportSecurity returns true if this connection type requires
// transport security.
//
// NOTE: This is part of the credentials.PerRPCCredentials interface.
func (c *NoiseConn) RequireTransportSecurity() bool {
	return true
}

// GetRequestMetadata returns the per RPC credentials encoded as gRPC metadata.
//
// NOTE: This is part of the credentials.PerRPCCredentials interface.
func (c *NoiseConn) GetRequestMetadata(_ context.Context,
	_ ...string) (map[string]string, error) {

	md := make(map[string]string)

	// The authentication data is just a string encoded representation of
	// HTTP header fields. So we can split by '\r\n' to get individual lines
	// and then by ': ' to get field name and field value.
	lines := strings.Split(string(c.authData), "\r\n")
	for _, line := range lines {
		parts := strings.Split(line, ": ")
		if len(parts) != 2 {
			continue
		}

		md[parts[0]] = parts[1]
	}
	return md, nil
}
