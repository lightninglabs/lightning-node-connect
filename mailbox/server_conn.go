package mailbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lightninglabs/terminal-connect/gbn"

	"github.com/lightninglabs/terminal-connect/hashmailrpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ServerConn is a type that implements the Conn interface and can be used to
// serve a gRPC connection over a noise encrypted connection that uses a mailbox
// connection as its transport connection. The mailbox level connection to the
// mailbox server uses native gRPC since the "server" in this case is lnd itself
// that's not running in a restricted environment such as the browser/WASM env.
type ServerConn struct {
	*connKit

	client hashmailrpc.HashMailClient

	gbnConn    *gbn.GoBackNConn
	gbnOptions []gbn.Option

	receiveBoxCreated bool
	receiveStream     hashmailrpc.HashMail_RecvStreamClient
	receiveStreamMu   sync.Mutex

	sendBoxCreated bool
	sendStream     hashmailrpc.HashMail_SendStreamClient
	sendStreamMu   sync.Mutex

	cancel func()

	quit      chan struct{}
	closeOnce sync.Once
}

// NewServerConn creates a new net.Conn compatible server connection that uses
// a gRPC based connection to tunnel traffic over a mailbox server.
func NewServerConn(ctx context.Context, serverHost string,
	client hashmailrpc.HashMailClient, receiveSID,
	sendSID [64]byte) (*ServerConn, error) {

	ctxc, cancel := context.WithCancel(ctx)

	c := &ServerConn{
		client: client,
		cancel: cancel,
		quit:   make(chan struct{}),
		gbnOptions: []gbn.Option{
			gbn.WithTimeout(gbnTimeout),
			gbn.WithHandshakeTimeout(gbnHandshakeTimeout),
			gbn.WithKeepalivePing(gbnServerPingTimeout),
		},
	}
	c.connKit = &connKit{
		ctx:        ctxc,
		serverAddr: serverHost,
		impl:       c,
		receiveSID: receiveSID,
		sendSID:    sendSID,
	}

	log.Debugf("ServerConn: creating gbn, waiting for sync")
	gbnConn, err := gbn.NewServerConn(
		ctxc, c.sendToStream, c.recvFromStream, c.gbnOptions...,
	)
	if err != nil {
		return nil, err
	}
	log.Debugf("ServerConn: done creating gbn")

	c.gbnConn = gbnConn

	return c, nil
}

// RefreshServerConn returns the same ServerConn object created in
// NewServerConn but creates a new quit channel as well as
// instantiates a new GoBN connection.
func RefreshServerConn(s *ServerConn) (*ServerConn, error) {
	s.receiveStreamMu.Lock()
	defer s.receiveStreamMu.Unlock()

	s.sendStreamMu.Lock()
	defer s.sendStreamMu.Unlock()

	sc := &ServerConn{
		client:            s.client,
		receiveBoxCreated: s.receiveBoxCreated,
		sendBoxCreated:    s.sendBoxCreated,
		gbnOptions:        s.gbnOptions,
		cancel:            s.cancel,
		quit:              make(chan struct{}),
	}

	// Ensure that the connKit is also re-created.
	sc.connKit = &connKit{
		ctx:        s.connKit.ctx,
		serverAddr: s.connKit.serverAddr,
		impl:       sc,
		receiveSID: s.connKit.receiveSID,
		sendSID:    s.connKit.sendSID,
	}

	log.Debugf("ServerConn: creating gbn")
	gbnConn, err := gbn.NewServerConn(
		sc.ctx, sc.sendToStream, sc.recvFromStream, sc.gbnOptions...,
	)
	if err != nil {
		return nil, err
	}

	sc.gbnConn = gbnConn
	log.Debugf("ServerConn: done creating gbn")

	return sc, nil
}

// recvFromStream is used to receive a payload from the receive stream.
func (c *ServerConn) recvFromStream(ctx context.Context) ([]byte, error) {
	c.receiveStreamMu.Lock()
	if c.receiveStream == nil {
		c.createReceiveMailBox(ctx, 0)
	}
	c.receiveStreamMu.Unlock()

	for {
		select {
		case <-c.quit:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		c.receiveStreamMu.Lock()
		controlMsg, err := c.receiveStream.Recv()
		if err != nil {
			log.Debugf("Server: got failure on receive socket, "+
				"re-trying: %v", err)

			c.createReceiveMailBox(ctx, retryWait)
			c.receiveStreamMu.Unlock()

			continue
		}
		c.receiveStreamMu.Unlock()
		return controlMsg.Msg, nil
	}
}

// sendToStream is used to send a payload on the send stream.
func (c *ServerConn) sendToStream(ctx context.Context, payload []byte) error {
	c.sendStreamMu.Lock()
	if c.sendStream == nil {
		c.createSendMailBox(ctx, 0)
	}
	c.sendStreamMu.Unlock()

	for {
		select {
		case <-c.quit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		c.sendStreamMu.Lock()
		err := c.sendStream.Send(&hashmailrpc.CipherBox{
			Desc: &hashmailrpc.CipherBoxDesc{
				StreamId: c.sendSID[:],
			},
			Msg: payload,
		})
		if err != nil {
			log.Debugf("Server: got failure on send socket, "+
				"re-trying: %v", err)

			c.createSendMailBox(ctx, retryWait)
			c.sendStreamMu.Unlock()

			continue
		}
		c.sendStreamMu.Unlock()
		return nil
	}
}

// ReceiveControlMsg tries to receive a control message over the underlying
// mailbox connection.
//
// NOTE: This is part of the Conn interface.
func (c *ServerConn) ReceiveControlMsg(receive ControlMsg) error {
	msg, err := c.gbnConn.Recv()
	if err != nil {
		return fmt.Errorf("error reading from go-back-n: %v", err)
	}

	if err := receive.Deserialize(msg); err != nil {
		return fmt.Errorf("error parsing control message: %v", err)
	}

	return nil
}

// SendControlMsg tries to send a control message over the underlying mailbox
// connection.
//
// NOTE: This is part of the Conn interface.
func (c *ServerConn) SendControlMsg(controlMsg ControlMsg) error {
	payload, err := controlMsg.Serialize()
	if err != nil {
		return fmt.Errorf("error serializing message: %v", err)
	}
	return c.gbnConn.Send(payload)
}

// createReceiveMailBox attempts to create a cipher box on the hashmail server
// and then to fetch the read stream of that cipher box. It retries until it
// succeeds or the ServerConn quits or the passed in context is canceled.
func (c *ServerConn) createReceiveMailBox(ctx context.Context,
	initialBackoff time.Duration) {

	waiter := gbn.NewBackoffWaiter(initialBackoff, gbnTimeout, retryWait)
	for {
		select {
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		default:
		}

		waiter.Wait()

		// Create receive mailbox and get receive stream.
		err := initAccountCipherBox(ctx, c.client, c.receiveSID)
		if err != nil && !isErrAlreadyExists(err) {
			log.Debugf("Server: failed to re-create read stream mbox: %v", err)

			continue
		}
		c.receiveBoxCreated = true

		// Fetch the receive stream. Note that since the receive
		// stream object is re-initialised by each connection, the
		// ctx parameter is used initialise the stream. This enables
		// the current connection to control the release of this stream
		// and exit if needed.
		streamDesc := &hashmailrpc.CipherBoxDesc{
			StreamId: c.receiveSID[:],
		}
		readStream, err := c.client.RecvStream(ctx, streamDesc)
		if err != nil {
			log.Debugf("Server: failed to create read stream: %w", err)

			continue
		}

		c.receiveStream = readStream

		log.Debugf("Server: receive mailbox created")
		return
	}
}

// createSendMailBox creates a cipher box on the hashmail server and fetches
// the send stream of the cipher box. It retries until it succeeds or until the
// ServerConn is closing or the passed context is canceled.
func (c *ServerConn) createSendMailBox(ctx context.Context,
	initialBackoff time.Duration) {

	waiter := gbn.NewBackoffWaiter(initialBackoff, gbnTimeout, retryWait)
	for {
		select {
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		default:
		}

		waiter.Wait()

		// Create send mailbox and get send stream.
		err := initAccountCipherBox(ctx, c.client, c.sendSID)
		if err != nil && !isErrAlreadyExists(err) {
			log.Debugf("error creating send cipher box: %v", err)
			continue
		}
		c.sendBoxCreated = true

		// Fetch the send stream. Note that since the receive
		// stream object is re-initialised by each connection, the
		// ctx parameter is used initialise the stream. This enables
		// the current connection to control the release of this stream
		// and exit if needed.
		writeStream, err := c.client.SendStream(ctx)
		if err != nil {
			log.Debugf("unable to create send stream: %w", err)
			continue
		}
		c.sendStream = writeStream

		log.Debugf("Server: Send mailbox created")
		return
	}
}

// Stop cleans up all resources of the ServerConn including deleting the
// mailboxes on the hash-mail server.
func (c *ServerConn) Stop() error {
	var returnErr error
	if err := c.Close(); err != nil {
		log.Errorf("error closing mailbox")
		returnErr = err
	}

	if c.receiveBoxCreated {
		if err := delCipherBox(c.ctx, c.client, c.receiveSID); err != nil {
			log.Errorf("error removing receive cipher box: %v", err)
			returnErr = err
		}
	}
	if c.sendBoxCreated {
		if err := delCipherBox(c.ctx, c.client, c.sendSID); err != nil {
			log.Errorf("error removing send cipher box: %v", err)
			returnErr = err
		}
	}

	c.cancel()
	return returnErr
}

// Close is part of the net.Conn interface that the grpc library will call
// after each connection is exiting. Therefore this function is used to
// cleanup any resources that need to be resent between connections.
// It closes the the current GoBN connection as well as releases the grpc
// streams. It does not delete the mailboxes since these will be used by
// the next connection. The streams, however, must be released so that they
// can be initialised with the context of the next connection. To completely
// shut down the ServerConn, Stop should be called.
//
// NOTE: This is part of the net.Conn interface.
func (c *ServerConn) Close() error {
	var returnErr error

	c.closeOnce.Do(func() {
		log.Debugf("Server connection is closing")

		if err := c.gbnConn.Close(); err != nil {
			log.Debugf("Error closing gbn connection in " +
				"server conn")
			returnErr = err
		}

		if c.receiveStream != nil {
			log.Debugf("closing receive stream")
			if err := c.receiveStream.CloseSend(); err != nil {
				log.Errorf("error closing receive stream: %v", err)
				returnErr = err
			}
		}

		if c.sendStream != nil {
			log.Debugf("closing send stream")
			if err := c.sendStream.CloseSend(); err != nil {
				log.Errorf("error closing send stream: %v", err)
				returnErr = err
			}
		}

		close(c.quit)
		log.Debugf("Server connection closed")
	})

	return returnErr
}

// Done returns the quit channel of the ServerConn and thus can be used
// to determine if the current connection is closed or not.
func (c *ServerConn) Done() <-chan struct{} {
	return c.quit
}

var _ ProxyConn = (*ServerConn)(nil)

// initAccountCipherBox attempts to initialize a new CipherBox using the
// account key as an authentication mechanism.
func initAccountCipherBox(ctx context.Context,
	hashMailClient hashmailrpc.HashMailClient, sid [64]byte) error {

	streamInit := &hashmailrpc.CipherBoxAuth{
		Desc: &hashmailrpc.CipherBoxDesc{
			StreamId: sid[:],
		},
		Auth: &hashmailrpc.CipherBoxAuth_LndAuth{
			LndAuth: &hashmailrpc.LndAuth{},
		},
	}
	_, err := hashMailClient.NewCipherBox(ctx, streamInit)
	return err
}

// delCipherBox attempts to initialize a new CipherBox using the
// account key as an authentication mechanism.
func delCipherBox(ctx context.Context,
	hashMailClient hashmailrpc.HashMailClient, sid [64]byte) error {

	streamDelete := &hashmailrpc.CipherBoxAuth{
		Desc: &hashmailrpc.CipherBoxDesc{
			StreamId: sid[:],
		},
		Auth: &hashmailrpc.CipherBoxAuth_LndAuth{
			LndAuth: &hashmailrpc.LndAuth{},
		},
	}
	_, err := hashMailClient.DelCipherBox(ctx, streamDelete)
	return err
}

// isErrAlreadyExists returns true if the passed error is the "already exists"
// error within the error wrapped error which is returned by the hash mail
// server when a stream we're attempting to create already exists.
func isErrAlreadyExists(err error) bool {
	statusCode, ok := status.FromError(err)
	if !ok {
		return false
	}

	return statusCode.Code() == codes.AlreadyExists
}
