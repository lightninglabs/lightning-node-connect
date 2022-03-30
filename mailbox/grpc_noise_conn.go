package mailbox

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
)

var _ credentials.TransportCredentials = (*NoiseGrpcConn)(nil)
var _ credentials.PerRPCCredentials = (*NoiseGrpcConn)(nil)

const (
	defaultGrpcWriteBufSize = 32 * 1024
)

// NoiseGrpcConn is a type that implements the credentials.TransportCredentials
// interface and can therefore be used as a replacement of the default TLS
// implementation that's used by HTTP/2.
type NoiseGrpcConn struct {
	ProxyConn
	proxyConnMtx sync.RWMutex

	connData *ConnData

	nextMsg    []byte
	nextMsgMtx sync.Mutex

	noise *Machine

	minHandshakeVersion byte
	maxHandshakeVersion byte
}

// NewNoiseGrpcConn creates a new noise connection using given local ECDH key.
// The auth data can be set for server connections and is sent as the payload
// to the client during the handshake.
func NewNoiseGrpcConn(connData *ConnData,
	options ...func(conn *NoiseGrpcConn)) *NoiseGrpcConn {

	conn := &NoiseGrpcConn{
		connData:            connData,
		minHandshakeVersion: MinHandshakeVersion,
		maxHandshakeVersion: MaxHandshakeVersion,
	}

	for _, opt := range options {
		opt(conn)
	}

	return conn
}

// WithMinHandshakeVersion is a functional option used to set the minimum
// handshake version supported.
func WithMinHandshakeVersion(version byte) func(*NoiseGrpcConn) {
	return func(conn *NoiseGrpcConn) {
		conn.minHandshakeVersion = version
	}
}

// WithMaxHandshakeVersion is a functional option used to set the maximum
// handshake version supported.
func WithMaxHandshakeVersion(version byte) func(*NoiseGrpcConn) {
	return func(conn *NoiseGrpcConn) {
		conn.maxHandshakeVersion = version
	}
}

// Read tries to read an encrypted data message from the underlying control
// connection and then tries to decrypt it.
//
// NOTE: This is part of the net.Conn interface.
func (c *NoiseGrpcConn) Read(b []byte) (n int, err error) {
	c.proxyConnMtx.RLock()
	defer c.proxyConnMtx.RUnlock()

	c.nextMsgMtx.Lock()
	defer c.nextMsgMtx.Unlock()

	// The last read was incomplete, return the few bytes that didn't fit.
	if len(c.nextMsg) > 0 {
		msgLen := len(c.nextMsg)
		copy(b, c.nextMsg)

		c.nextMsg = nil
		return msgLen, nil
	}

	requestBytes, err := c.noise.ReadMessage(c.ProxyConn)
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
func (c *NoiseGrpcConn) Write(b []byte) (int, error) {
	c.proxyConnMtx.RLock()
	defer c.proxyConnMtx.RUnlock()

	err := c.noise.WriteMessage(b)
	if err != nil {
		return 0, err
	}

	return c.noise.Flush(c.ProxyConn)
}

// LocalAddr returns the local address of this connection.
//
// NOTE: This is part of the Conn interface.
func (c *NoiseGrpcConn) LocalAddr() net.Addr {
	c.proxyConnMtx.RLock()
	defer c.proxyConnMtx.RUnlock()

	if c.ProxyConn == nil {
		return &NoiseAddr{PubKey: c.connData.LocalKey().PubKey()}
	}

	return &NoiseAddr{
		PubKey: c.connData.LocalKey().PubKey(),
		Server: c.ProxyConn.LocalAddr().String(),
	}
}

// RemoteAddr returns the remote address of this connection.
//
// NOTE: This is part of the Conn interface.
func (c *NoiseGrpcConn) RemoteAddr() net.Addr {
	c.proxyConnMtx.RLock()
	defer c.proxyConnMtx.RUnlock()

	if c.ProxyConn == nil {
		return &NoiseAddr{PubKey: c.connData.RemoteKey()}
	}

	return &NoiseAddr{
		PubKey: c.connData.RemoteKey(),
		Server: c.ProxyConn.RemoteAddr().String(),
	}
}

// ClientHandshake implements the client side part of the noise connection
// handshake.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseGrpcConn) ClientHandshake(_ context.Context, _ string,
	conn net.Conn) (net.Conn, credentials.AuthInfo, error) {

	c.proxyConnMtx.Lock()
	defer c.proxyConnMtx.Unlock()

	log.Tracef("Starting client handshake")

	transportConn, ok := conn.(ProxyConn)
	if !ok {
		return nil, nil, fmt.Errorf("invalid connection type")
	}
	c.ProxyConn = transportConn

	// First, initialize a new noise machine with our static long term, and
	// password.
	var err error
	c.noise, err = NewBrontideMachine(&BrontideMachineConfig{
		Initiator:           true,
		HandshakePattern:    c.connData.HandshakePattern(),
		ConnData:            c.connData,
		MinHandshakeVersion: c.minHandshakeVersion,
		MaxHandshakeVersion: c.maxHandshakeVersion,
	})
	if err != nil {
		return nil, nil, err
	}

	log.Debugf("Kicking off client handshake with client_key=%x",
		c.connData.LocalKey().PubKey().SerializeCompressed())

	// We'll ensure that we get a response from the remote peer in a timely
	// manner. If they don't respond within 1s, then we'll kill the
	// connection.
	err = c.ProxyConn.SetReadDeadline(time.Now().Add(handshakeReadTimeout))
	if err != nil {
		return nil, nil, err
	}

	if err := c.noise.DoHandshake(c.ProxyConn); err != nil {
		return nil, nil, err
	}

	// We'll reset the deadline as it's no longer critical beyond the
	// initial handshake.
	err = c.ProxyConn.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, nil, err
	}

	log.Debugf("Completed client handshake with with server_key=%x",
		c.noise.remoteStatic.SerializeCompressed())

	log.Tracef("Client handshake completed")

	return c, NewAuthInfo(), nil
}

// ServerHandshake implements the server part of the noise connection handshake.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseGrpcConn) ServerHandshake(conn net.Conn) (net.Conn,
	credentials.AuthInfo, error) {

	c.proxyConnMtx.Lock()
	defer c.proxyConnMtx.Unlock()

	log.Tracef("Starting server handshake")

	transportConn, ok := conn.(ProxyConn)
	if !ok {
		return nil, nil, fmt.Errorf("invalid connection type")
	}
	c.ProxyConn = transportConn

	// First, we'll initialize a new state machine with our static key,
	// remote static key, passphrase, and also the authentication data.
	var err error
	c.noise, err = NewBrontideMachine(&BrontideMachineConfig{
		Initiator:           false,
		HandshakePattern:    c.connData.HandshakePattern(),
		ConnData:            c.connData,
		MinHandshakeVersion: c.minHandshakeVersion,
		MaxHandshakeVersion: c.maxHandshakeVersion,
	})
	if err != nil {
		return nil, nil, err
	}

	// We'll ensure that we get a response from the remote peer in a timely
	// manner. If they don't respond within 1s, then we'll kill the
	// connection.
	err = c.ProxyConn.SetReadDeadline(time.Now().Add(handshakeReadTimeout))
	if err != nil {
		return nil, nil, err
	}

	if err := c.noise.DoHandshake(c.ProxyConn); err != nil {
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
func (c *NoiseGrpcConn) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{
		ProtocolVersion:  fmt.Sprintf("%d", ProtocolVersion),
		SecurityProtocol: ProtocolName,
		ServerName:       "lnd",
	}
}

// Close ensures that we hold a lock on the ProxyConn before calling close on
// it to ensure that the handshake functions don't use the ProxyConn at the same
// time.
//
// NOTE: This is part of the net.Conn interface.
func (c *NoiseGrpcConn) Close() error {
	c.proxyConnMtx.RLock()
	defer c.proxyConnMtx.RUnlock()

	return c.ProxyConn.Close()
}

// Clone makes a copy of this TransportCredentials.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseGrpcConn) Clone() credentials.TransportCredentials {
	c.proxyConnMtx.RLock()
	defer c.proxyConnMtx.RUnlock()

	return &NoiseGrpcConn{
		ProxyConn: c.ProxyConn,
		connData:  c.connData,
	}
}

// OverrideServerName overrides the server name used to verify the hostname on
// the returned certificates from the server.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (c *NoiseGrpcConn) OverrideServerName(_ string) error {
	return nil
}

// RequireTransportSecurity returns true if this connection type requires
// transport security.
//
// NOTE: This is part of the credentials.PerRPCCredentials interface.
func (c *NoiseGrpcConn) RequireTransportSecurity() bool {
	return true
}

// GetRequestMetadata returns the per RPC credentials encoded as gRPC metadata.
//
// NOTE: This is part of the credentials.PerRPCCredentials interface.
func (c *NoiseGrpcConn) GetRequestMetadata(_ context.Context,
	_ ...string) (map[string]string, error) {

	md := make(map[string]string)

	// The authentication data is just a string encoded representation of
	// HTTP header fields. So we can split by '\r\n' to get individual lines
	// and then by ': ' to get field name and field value.
	lines := strings.Split(string(c.connData.AuthData()), "\r\n")
	for _, line := range lines {
		parts := strings.Split(line, ": ")
		if len(parts) != 2 {
			continue
		}

		md[parts[0]] = parts[1]
	}
	return md, nil
}
