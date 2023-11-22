package mailbox

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"google.golang.org/grpc"
)

// ClientOption is the signature of a Client functional option.
type ClientOption func(*Client)

// WithGrpcConn initialised the grpc client of the Client using the given
// connection.
func WithGrpcConn(conn *grpc.ClientConn) ClientOption {
	return func(client *Client) {
		client.grpcClient = hashmailrpc.NewHashMailClient(conn)
	}
}

// Client manages the mailboxConn it holds and refreshes it on connection
// retries.
type Client struct {
	serverHost string
	connData   *ConnData

	mailboxConn *ClientConn

	grpcClient hashmailrpc.HashMailClient

	status   ClientStatus
	statusMu sync.Mutex

	sid [64]byte

	ctx context.Context //nolint:containedctx

	log btclog.Logger
}

// NewClient creates a new Client object which will handle the mailbox
// connection.
func NewClient(ctx context.Context, serverHost string, connData *ConnData,
	opts ...ClientOption) (*Client, error) {

	sid, err := connData.SID()
	if err != nil {
		return nil, err
	}

	c := &Client{
		ctx:        ctx,
		serverHost: serverHost,
		connData:   connData,
		status:     ClientStatusNotConnected,
		sid:        sid,
		log:        newPrefixedLogger(false),
	}

	// Apply functional options.
	for _, o := range opts {
		o(c)
	}

	return c, nil
}

// NewGrpcClient creates a new Client object which will handle the mailbox
// connection and will use grpc streams to connect to the mailbox.
func NewGrpcClient(ctx context.Context, serverHost string, connData *ConnData,
	dialOpts ...grpc.DialOption) (*Client, error) {

	mailboxGrpcConn, err := grpc.Dial(serverHost, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to RPC server: %w",
			err)
	}

	return NewClient(
		ctx, serverHost, connData, WithGrpcConn(mailboxGrpcConn),
	)
}

// NewWebsocketsClient creates a new Client object which will handle the mailbox
// connection and will use websockets to connect to the mailbox.
func NewWebsocketsClient(ctx context.Context, serverHost string,
	connData *ConnData) (*Client, error) {

	return NewClient(ctx, serverHost, connData)
}

// Dial returns a net.Conn abstraction over the mailbox connection. Dial is
// called everytime grpc retries the connection. If this is the first
// connection, a new ClientConn will be created. Otherwise, the existing
// connection will just be refreshed.
func (c *Client) Dial(_ context.Context, _ string) (net.Conn, error) {
	// If there is currently an active connection, block here until the
	// previous connection as been closed.
	if c.mailboxConn != nil {
		c.log.Debugf("Dial: have existing mailbox connection, waiting")
		<-c.mailboxConn.Done()
		c.log.Debugf("Dial: done with existing conn")
	}

	c.log.Debugf("Dialing...")

	sid, err := c.connData.SID()
	if err != nil {
		return nil, err
	}

	// If the SID has changed from what it was previously, then we close any
	// previous connection we had.
	if !bytes.Equal(c.sid[:], sid[:]) && c.mailboxConn != nil {
		err := c.mailboxConn.Close()
		if err != nil {
			c.log.Errorf("Could not close mailbox conn: %v", err)
		}

		c.mailboxConn = nil
	}

	c.sid = sid

	if c.mailboxConn == nil {
		mailboxConn, err := NewClientConn(
			c.ctx, c.sid, c.serverHost, c.grpcClient,
			c.log, func(status ClientStatus) {
				c.statusMu.Lock()
				c.status = status
				c.statusMu.Unlock()
			},
		)
		if err != nil {
			return nil, &temporaryError{err}
		}
		c.mailboxConn = mailboxConn
	} else {
		mailboxConn, err := RefreshClientConn(c.ctx, c.mailboxConn)
		if err != nil {
			return nil, &temporaryError{err}
		}
		c.mailboxConn = mailboxConn
	}

	return c.mailboxConn, nil
}

// ConnStatus returns last determined client connection status.
func (c *Client) ConnStatus() ClientStatus {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()

	return c.status
}
