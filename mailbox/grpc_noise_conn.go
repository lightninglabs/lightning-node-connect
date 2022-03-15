package mailbox

import (
	"context"
	"fmt"
	"net"
	"sync"

	"google.golang.org/grpc/credentials"
)

const (
	defaultGrpcWriteBufSize = 32 * 1024
)

// NoiseGrpcConn is a type that implements the credentials.TransportCredentials
// interface and can therefore be used as a replacement of the default TLS
// implementation that's used by HTTP/2.
type NoiseGrpcConn struct {
	ProxyConn
	proxyConnMtx sync.RWMutex

	nextMsg    []byte
	nextMsgMtx sync.Mutex

	noise *Machine
}

// NewNoiseGrpcConn creates a new noise connection using given local ECDH key.
// The auth data can be set for server connections and is sent as the payload
// to the client during the handshake.
func NewNoiseGrpcConn(conn ProxyConn, noise *Machine) *NoiseGrpcConn {
	return &NoiseGrpcConn{
		ProxyConn: conn,
		noise:     noise,
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
		return &NoiseAddr{PubKey: c.noise.localStatic.PubKey()}
	}

	return &NoiseAddr{
		PubKey: c.noise.localStatic.PubKey(),
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
		return &NoiseAddr{PubKey: c.noise.remoteStatic}
	}

	return &NoiseAddr{
		PubKey: c.noise.remoteStatic,
		Server: c.ProxyConn.RemoteAddr().String(),
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

var _ credentials.TransportCredentials = (*FakeCredentials)(nil)

type FakeCredentials struct{}

func (f *FakeCredentials) ClientHandshake(_ context.Context, _ string,
	conn net.Conn) (net.Conn, credentials.AuthInfo, error) {

	return conn, NewAuthInfo(), nil
}

func (f *FakeCredentials) ServerHandshake(conn net.Conn) (net.Conn,
	credentials.AuthInfo, error) {

	return conn, NewAuthInfo(), nil
}

// Info returns general information about the protocol that's being used for
// this connection.
//
// NOTE: This is part of the credentials.TransportCredentials interface.
func (f *FakeCredentials) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{
		ProtocolVersion:  fmt.Sprintf("%d", ProtocolVersion),
		SecurityProtocol: ProtocolName,
		ServerName:       "lnd",
	}
}

func (f *FakeCredentials) Clone() credentials.TransportCredentials {
	return &FakeCredentials{}
}

func (f *FakeCredentials) OverrideServerName(_ string) error {
	return nil
}
