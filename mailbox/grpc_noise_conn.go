package mailbox

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"

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
	MailboxConn

	localNonce  uint64
	remoteNonce uint64

	secret   [32]byte
	authData []byte

	localKey  keychain.SingleKeyECDH
	remoteKey *btcec.PublicKey

	nextMsg    []byte
	nextMsgMtx sync.Mutex
}

// NewNoiseConn creates a new noise connection using given local ECDH key. The
// auth data can be set for server connections and is sent as the payload to the
// client during the handshake.
func NewNoiseConn(localKey keychain.SingleKeyECDH,
	authData []byte) *NoiseConn {

	return &NoiseConn{
		localKey: localKey,
		authData: authData,
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

	msg := NewMsgData(ProtocolVersion, nil)
	if err := c.MailboxConn.ReceiveControlMsg(msg); err != nil {
		return 0, fmt.Errorf("error receiving data msg: %v", err)
	}

	requestBytes, err := c.Decrypt(msg.Payload, c.secret[:])
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
	payload, err := c.Encrypt(b, c.secret[:])
	if err != nil {
		return 0, fmt.Errorf("error encrypting response: %v", err)
	}

	msg := NewMsgData(ProtocolVersion, payload)
	if err := c.MailboxConn.SendControlMsg(msg); err != nil {
		return 0, fmt.Errorf("error sending data msg: %v", err)
	}

	return len(b), nil
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

// resetCipherState resets the internal state of the noise machine so we can
// properly reconnect and do the main handshake again.
func (c *NoiseConn) resetCipherState() {
	c.localNonce = 0
	c.remoteNonce = 0
}

// ClientHandshake implements the client side part of the noise connection
// handshake.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseConn) ClientHandshake(_ context.Context, _ string,
	conn net.Conn) (net.Conn, credentials.AuthInfo, error) {

	// Ensure the cipher state is reset each time we need to do the
	// handshake.
	c.resetCipherState()

	log.Tracef("Starting client handshake")

	transportConn, ok := conn.(MailboxConn)
	if !ok {
		return nil, nil, fmt.Errorf("invalid connection type")
	}
	c.MailboxConn = transportConn

	clientHello := NewMsgClientHello(ProtocolVersion, c.localKey.PubKey())
	if err := c.MailboxConn.SendControlMsg(clientHello); err != nil {
		return nil, nil, err
	}
	log.Debugf("Sent client hello with client_key=%x",
		c.localKey.PubKey().SerializeCompressed())

	serverHello := NewMsgServerHello(ProtocolVersion, nil, nil)
	if err := c.MailboxConn.ReceiveControlMsg(serverHello); err != nil {
		return nil, nil, err
	}
	log.Debugf("Received server hello with server_key=%x",
		serverHello.PubKey.SerializeCompressed())

	var err error
	c.remoteKey = serverHello.PubKey
	c.secret, err = c.localKey.ECDH(c.remoteKey)
	if err != nil {
		return nil, nil, fmt.Errorf("error calculating shared "+
			"secret: %v", err)
	}

	authDataBytes, err := c.Decrypt(serverHello.AuthData, c.secret[:])
	if err != nil {
		return nil, nil, fmt.Errorf("error decrypting auth data: %v",
			err)
	}
	c.authData = authDataBytes

	log.Tracef("Client handshake completed")

	return c, NewAuthInfo(), nil
}

// ServerHandshake implements the server part of the noise connection handshake.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseConn) ServerHandshake(conn net.Conn) (net.Conn,
	credentials.AuthInfo, error) {

	// Ensure the cipher state is reset each time we need to do the
	// handshake.
	c.resetCipherState()

	log.Tracef("Starting server handshake")

	transportConn, ok := conn.(MailboxConn)
	if !ok {
		return nil, nil, fmt.Errorf("invalid connection type")
	}
	c.MailboxConn = transportConn

	clientHello := NewMsgClientHello(ProtocolVersion, nil)
	if err := c.MailboxConn.ReceiveControlMsg(clientHello); err != nil {
		return nil, nil, fmt.Errorf("error receiving client hello: %v",
			err)
	}
	c.remoteKey = clientHello.PubKey

	var err error
	c.secret, err = c.localKey.ECDH(c.remoteKey)
	if err != nil {
		return nil, nil, fmt.Errorf("error calculating shared "+
			"secret: %v", err)
	}

	log.Debugf("Received client hello msg, client_key=%x",
		clientHello.PubKey.SerializeCompressed())

	encryptedMac, err := c.Encrypt(c.authData, c.secret[:])
	if err != nil {
		return nil, nil, fmt.Errorf("error encrypting macaroon: %v",
			err)
	}

	serverHello := NewMsgServerHello(
		ProtocolVersion, c.localKey.PubKey(), encryptedMac,
	)
	if err := c.MailboxConn.SendControlMsg(serverHello); err != nil {
		return nil, nil, fmt.Errorf("error sending server hello: %v",
			err)
	}

	log.Tracef("Server handshake completed")

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
		secret:      c.secret,
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

func (c *NoiseConn) Encrypt(plainText []byte, secret []byte) ([]byte, error) {
	defer func() {
		c.localNonce++
	}()

	cipher, _ := chacha20poly1305.New(secret)

	var nonce [12]byte
	binary.LittleEndian.PutUint64(nonce[4:], c.localNonce)

	return cipher.Seal(nil, nonce[:], plainText, nil), nil
}

func (c *NoiseConn) Decrypt(cipherText []byte, secret []byte) ([]byte, error) {
	defer func() {
		c.remoteNonce++
	}()

	cipher, _ := chacha20poly1305.New(secret)

	var nonce [12]byte
	binary.LittleEndian.PutUint64(nonce[4:], c.remoteNonce)

	return cipher.Open(nil, nonce[:], cipherText, nil)
}
