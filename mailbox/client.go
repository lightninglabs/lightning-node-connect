package mailbox

import (
	"context"
	"net"
)

// Client manages the mailboxConn it holds and refreshes it on connection
// retries.
type Client struct {
	mailboxConn *ClientConn

	ctx context.Context
	sid [64]byte
}

// NewClient creates a new Client object which will handle the mailbox
// connection.
func NewClient(ctx context.Context, sid [64]byte) (*Client, error) {
	return &Client{
		ctx: ctx,
		sid: sid,
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

	return c.mailboxConn, nil
}
