package mailbox

import (
	"context"
	"fmt"
	"net"
)

// Client manages the switchConn it holds and refreshes it on connection
// retries.
type Client struct {
	switchConfig *SwitchConfig
	switchConn   *SwitchConn

	ctx context.Context
}

// NewClient creates a new Client object which will handle the mailbox
// connection.
func NewClient(ctx context.Context, serverHost string, sid [64]byte) (*Client,
	error) {

	return &Client{
		ctx: ctx,
		switchConfig: &SwitchConfig{
			ServerHost: serverHost,
			SID:        sid,
			NewProxyConn: func(sid [64]byte) (ProxyConn, error) {
				return NewClientConn(ctx, sid, serverHost)
			},
			RefreshProxyConn: func(conn ProxyConn) (ProxyConn, error) {
				clientConn, ok := conn.(*ClientConn)
				if !ok {
					return nil, fmt.Errorf("conn not of type " +
						"ClientConn")
				}

				return RefreshClientConn(clientConn)
			},
			StopProxyConn: func(conn ProxyConn) error {
				clientConn, ok := conn.(*ClientConn)
				if !ok {
					return fmt.Errorf("conn not of type " +
						"ClientConn")
				}

				return clientConn.Close()
			},
		},
	}, nil
}

// Dial returns a net.Conn abstraction over the mailbox connection. Dial is
// called everytime grpc retries the connection. If this is the first
// connection, a new ClientConn will be created. Otherwise, the existing
// connection will just be refreshed.
func (c *Client) Dial(_ context.Context, _ string) (net.Conn, error) {
	// If there is currently an active connection, block here until the
	// previous connection as been closed.
	if c.switchConn != nil {
		log.Debugf("Dial: have existing mailbox connection, waiting")
		<-c.switchConn.Done()
		log.Debugf("Dial: done with existing conn")
	}

	log.Debugf("Client: Dialing...")

	switchConn, err := NextSwitchConn(c.switchConn, c.switchConfig)
	if err != nil {
		return nil, &temporaryError{err}
	}

	c.switchConn = switchConn

	return c.switchConn, nil
}
