package mailbox

import (
	"context"
	"crypto/sha512"
	"net"

	"github.com/lightningnetwork/lnd/keychain"
)

// Client manages the mailboxConn it holds and refreshes it on connection
// retries.
type Client struct {
	mailboxConn *ClientConn

	noiseConn *NoiseGrpcConn

	ctx context.Context
	sid [64]byte
}

// NewClient creates a new Client object which will handle the mailbox
// connection.
func NewClient(ctx context.Context, localKey keychain.SingleKeyECDH,
	password []byte) (*Client, error) {

	sid := sha512.Sum512(password[:])
	noiseConn := NewNoiseGrpcConn(localKey, nil, password[:])

	return &Client{
		ctx:       ctx,
		noiseConn: noiseConn,
		sid:       sid,
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

	if err := c.noiseConn.ClientHandshake(c.mailboxConn); err != nil {
		return nil, &temporaryError{err}
	}

	return c.noiseConn, nil
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
func (c *Client) GetRequestMetadata(ctx context.Context,
	uri ...string) (map[string]string, error) {

	return c.noiseConn.GetRequestMetadata(ctx, uri...)
}
