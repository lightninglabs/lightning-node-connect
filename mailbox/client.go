package mailbox

import (
	"context"
	"crypto/sha512"
	"net"
	"strings"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightningnetwork/lnd/keychain"
)

// Client manages the mailboxConn it holds and refreshes it on connection
// retries.
type Client struct {
	mailboxConn *ClientConn

	noiseConn *NoiseGrpcConn

	hc *HandshakeController

	ctx context.Context
	sid [64]byte
}

// NewClient creates a new Client object which will handle the mailbox
// connection.
func NewClient(ctx context.Context, localKey keychain.SingleKeyECDH,
	password []byte) (*Client, error) {

	sid := sha512.Sum512(password[:])

	hs := NewHandshakeController(true, localKey, nil, nil, password, nil, nil, nil)

	return &Client{
		ctx: ctx,
		sid: sid,
		hc:  hs,
	}, nil
}

// Dial returns a net.Conn abstraction over the mailbox connection. Dial is
// called everytime grpc retries the connection. If this is the first
// connection, a new ClientConn will be created. Otherwise, the existing
// connection will just be refreshed.
func (c *Client) Dial(_ context.Context, serverHost string) (net.Conn, error) {
	// If there is currently an active connection, block here until the
	// previous connection as been closed.
	if c.mailboxConn != nil {
		log.Debugf("Dial: have existing mailbox connection, waiting")
		<-c.mailboxConn.Done()
		log.Debugf("Dial: done with existing conn")
	}

	log.Debugf("Client: Dialing...")
	if c.mailboxConn == nil {
		mailboxConn, err := NewClientConn(c.ctx, c.sid, serverHost)
		if err != nil {
			return nil, &temporaryError{err}
		}
		c.mailboxConn = mailboxConn
	} else {
		mailboxConn, err := RefreshClientConn(c.mailboxConn)
		if err != nil {
			return nil, &temporaryError{err}
		}
		c.mailboxConn = mailboxConn
	}

	c.hc.getConn = func(_ keychain.SingleKeyECDH, _ *btcec.PublicKey,
		_ []byte) net.Conn {

		return c.mailboxConn
	}

	c.hc.onRemoteStatic = func(key *btcec.PublicKey) {}

	noise, _, err := c.hc.doHandshake()
	if err != nil {
		return nil, &temporaryError{err}
	}

	return &NoiseGrpcConn{
		ProxyConn: c.mailboxConn,
		noise:     noise,
	}, nil
}

// RequireTransportSecurity returns true if this connection type requires
// transport security.
//
// NOTE: This is part of the credentials.PerRPCCredentials interface.
func (c *Client) RequireTransportSecurity() bool {
	return true
}

// GetRequestMetadata returns the per RPC credentials encoded as gRPC metadata.
//
// NOTE: This is part of the credentials.PerRPCCredentials interface.
func (c *Client) GetRequestMetadata(_ context.Context,
	_ ...string) (map[string]string, error) {

	md := make(map[string]string)

	// The authentication data is just a string encoded representation of
	// HTTP header fields. So we can split by '\r\n' to get individual lines
	// and then by ': ' to get field name and field value.
	lines := strings.Split(string(c.hc.authData), "\r\n")
	for _, line := range lines {
		parts := strings.Split(line, ": ")
		if len(parts) != 2 {
			continue
		}

		md[parts[0]] = parts[1]
	}
	return md, nil
}
