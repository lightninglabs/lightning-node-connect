package mailbox

import (
	"context"
	"fmt"
	"github.com/btcsuite/btcd/btcec"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc/credentials"
	"net"
	"strings"
)

var _ credentials.TransportCredentials = (*NoiseConn)(nil)
var _ credentials.PerRPCCredentials = (*NoiseConn)(nil)

// NoiseConn is a type that implements the credentials.TransportCredentials
// interface and can therefore be used as a replacement of the default TLS
// implementation that's used by HTTP/2.
type NoiseConn struct {
	Conn

	secret   [32]byte
	authData []byte

	localKey  keychain.SingleKeyECDH
	remoteKey *btcec.PublicKey
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
	msg := NewMsgData(ProtocolVersion, nil)
	if err := c.Conn.ReceiveControlMsg(msg); err != nil {
		return 0, fmt.Errorf("error receiving data msg: %v", err)
	}

	requestBytes, err := Decrypt(msg.Payload, c.secret[:])
	if err != nil {
		return 0, fmt.Errorf("error decrypting payload: %v", err)
	}

	copy(b, requestBytes)
	return len(requestBytes), nil
}

// Write encrypts the given application level payload and sends it as a data
// message over the underlying control connection.
//
// NOTE: This is part of the net.Conn interface.
func (c *NoiseConn) Write(b []byte) (int, error) {
	log.Debugf("len bytes pre noise %d", len(b))
	payload, err := Encrypt(b, c.secret[:])
	if err != nil {
		return 0, fmt.Errorf("error encrypting response: %v", err)
	}

	msg := NewMsgData(ProtocolVersion, payload)
	if err := c.Conn.SendControlMsg(msg); err != nil {
		return 0, fmt.Errorf("error sending data msg: %v", err)
	}

	return len(b), nil
}

// LocalAddr returns the local address of this connection.
//
// NOTE: This is part of the Conn interface.
func (c *NoiseConn) LocalAddr() net.Addr {
	if c.Conn == nil {
		return &NoiseAddr{PubKey: c.localKey.PubKey()}
	}

	return &NoiseAddr{
		PubKey: c.localKey.PubKey(),
		Server: c.Conn.LocalAddr().String(),
	}
}

// RemoteAddr returns the remote address of this connection.
//
// NOTE: This is part of the Conn interface.
func (c *NoiseConn) RemoteAddr() net.Addr {
	if c.Conn == nil {
		return &NoiseAddr{PubKey: c.remoteKey}
	}

	return &NoiseAddr{
		PubKey: c.remoteKey,
		Server: c.Conn.RemoteAddr().String(),
	}
}

// ClientHandshake implements the client side part of the noise connection
// handshake.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseConn) ClientHandshake(_ context.Context, _ string,
	conn net.Conn) (net.Conn, credentials.AuthInfo, error) {

	log.Tracef("Starting client handshake")

	transportConn, ok := conn.(Conn)
	if !ok {
		return nil, nil, fmt.Errorf("invalid connection type")
	}
	c.Conn = transportConn

	clientHello := NewMsgClientHello(ProtocolVersion, c.localKey.PubKey())
	if err := c.Conn.SendControlMsg(clientHello); err != nil {
		return nil, nil, err
	}
	log.Debugf("Sent client hello with client_key=%x",
		c.localKey.PubKey().SerializeCompressed())

	serverHello := NewMsgServerHello(ProtocolVersion, nil, nil)
	if err := c.Conn.ReceiveControlMsg(serverHello); err != nil {
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

	authDataBytes, err := Decrypt(serverHello.AuthData, c.secret[:])
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

	log.Tracef("Starting server handshake")

	transportConn, ok := conn.(Conn)
	if !ok {
		return nil, nil, fmt.Errorf("invalid connection type")
	}
	c.Conn = transportConn

	clientHello := NewMsgClientHello(ProtocolVersion, nil)
	if err := c.Conn.ReceiveControlMsg(clientHello); err != nil {
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

	encryptedMac, err := Encrypt(c.authData, c.secret[:])
	if err != nil {
		return nil, nil, fmt.Errorf("error encrypting macaroon: %v",
			err)
	}

	serverHello := NewMsgServerHello(
		ProtocolVersion, c.localKey.PubKey(), encryptedMac,
	)
	if err := c.Conn.SendControlMsg(serverHello); err != nil {
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
		Conn:      c.Conn,
		secret:    c.secret,
		authData:  c.authData,
		localKey:  c.localKey,
		remoteKey: c.remoteKey,
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
