package mailbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/lightning-node-connect/gbn"
	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ServerStatus is a description of the server's session state.
type ServerStatus uint8

const (
	// ServerStatusNotConnected means that the server is currently not
	// connected to a mailbox. This is either because the session is
	// restarting or because the mailbox server is down.
	ServerStatusNotConnected ServerStatus = 0

	// ServerStatusInUse means that a client is currently connected and
	// using the session.
	ServerStatusInUse ServerStatus = 1

	// ServerStatusIdle means that the session is ready for use but that
	// there is currently no client connected.
	ServerStatusIdle ServerStatus = 2
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

	status      ServerStatus
	onNewStatus func(status ServerStatus)
	statusMu    sync.Mutex

	log btclog.Logger

	cancel func()

	quit      chan struct{}
	closeOnce sync.Once
}

// NewServerConn creates a new net.Conn compatible server connection that uses
// a gRPC based connection to tunnel traffic over a mailbox server.
func NewServerConn(ctx context.Context, serverHost string,
	client hashmailrpc.HashMailClient, sid [64]byte, logger btclog.Logger,
	onNewStatus func(status ServerStatus)) (*ServerConn, error) {

	ctxc, cancel := context.WithCancel(ctx)

	receiveSID := GetSID(sid, false)
	sendSID := GetSID(sid, true)

	c := &ServerConn{
		client: client,
		cancel: cancel,
		quit:   make(chan struct{}),
		gbnOptions: []gbn.Option{
			gbn.WithTimeoutOptions(
				gbn.WithStaticResendTimeout(gbnTimeout),
				gbn.WithHandshakeTimeout(gbnHandshakeTimeout),
				gbn.WithKeepalivePing(
					gbnServerPingTimeout, gbnPongTimeout,
				),
			),
		},
		status:      ServerStatusNotConnected,
		log:         logger,
		onNewStatus: onNewStatus,
	}
	c.connKit = &connKit{
		ctx:        ctxc,
		serverAddr: serverHost,
		impl:       c,
		receiveSID: receiveSID,
		sendSID:    sendSID,
	}

	logger.Debugf("Creating gbn, waiting for sync")
	var err error
	c.gbnConn, err = gbn.NewServerConn(
		ctxc, c.sendToStream, c.recvFromStream, c.gbnOptions...,
	)
	if err != nil {
		return nil, err
	}
	logger.Debugf("Done creating gbn")

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

	s.statusMu.Lock()
	defer s.statusMu.Unlock()

	sc := &ServerConn{
		client:            s.client,
		receiveBoxCreated: s.receiveBoxCreated,
		sendBoxCreated:    s.sendBoxCreated,
		gbnOptions:        s.gbnOptions,
		cancel:            s.cancel,
		status:            ServerStatusNotConnected,
		onNewStatus:       s.onNewStatus,
		log:               s.log,
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

	s.log.Debugf("ServerConn: creating gbn")
	var err error
	sc.gbnConn, err = gbn.NewServerConn(
		sc.ctx, sc.sendToStream, sc.recvFromStream, sc.gbnOptions...,
	)
	if err != nil {
		return nil, err
	}

	s.log.Debugf("ServerConn: done creating gbn")

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
			c.log.Debugf("Got failure on receive socket, "+
				"re-trying: %v", err)

			c.setStatus(ServerStatusNotConnected)
			c.createReceiveMailBox(ctx, retryWait)
			c.receiveStreamMu.Unlock()

			continue
		}
		c.receiveStreamMu.Unlock()

		c.setStatus(ServerStatusInUse)
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
			c.log.Debugf("Got failure on send socket, "+
				"re-trying: %v", err)

			c.setStatus(ServerStatusNotConnected)
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

// SetRecvTimeout sets the timeout to be used when attempting to receive data.
func (c *ServerConn) SetRecvTimeout(timeout time.Duration) {
	c.gbnConn.SetRecvTimeout(timeout)
}

// SetSendTimeout sets the timeout to be used when attempting to send data.
func (c *ServerConn) SetSendTimeout(timeout time.Duration) {
	c.gbnConn.SetSendTimeout(timeout)
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
			c.log.Debugf("Failed to re-create read stream mbox: %v",
				err)

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
			c.log.Debugf("Failed to create read stream: %w", err)

			continue
		}

		c.setStatus(ServerStatusIdle)
		c.receiveStream = readStream

		c.log.Debugf("Receive mailbox created")

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
			c.log.Debugf("Error creating send cipher box: %v", err)

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
			c.log.Debugf("Unable to create send stream: %w", err)

			continue
		}
		c.sendStream = writeStream

		c.log.Debugf("Send mailbox created")

		return
	}
}

// Stop cleans up all resources of the ServerConn including deleting the
// mailboxes on the hash-mail server.
func (c *ServerConn) Stop() error {
	var returnErr error
	if err := c.Close(); err != nil {
		c.log.Errorf("Error closing mailbox")

		returnErr = err
	}

	if c.receiveBoxCreated {
		err := delCipherBox(c.ctx, c.client, c.receiveSID)
		if err != nil {
			c.log.Errorf("Error removing receive cipher box: %v",
				err)

			returnErr = err
		}
	}
	if c.sendBoxCreated {
		if err := delCipherBox(c.ctx, c.client, c.sendSID); err != nil {
			c.log.Errorf("Error removing send cipher box: %v", err)
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
		c.log.Debugf("Connection is closing")

		if c.gbnConn != nil {
			if err := c.gbnConn.Close(); err != nil {
				c.log.Debugf("Error closing gbn connection " +
					"in server conn")

				returnErr = err
			}
		}

		if c.receiveStream != nil {
			c.log.Debugf("Closing receive stream")
			if err := c.receiveStream.CloseSend(); err != nil {
				c.log.Errorf("Error closing receive stream: %v",
					err)

				returnErr = err
			}
		}

		if c.sendStream != nil {
			c.log.Debugf("Closing send stream")
			if err := c.sendStream.CloseSend(); err != nil {
				c.log.Errorf("Error closing send stream: %v",
					err)

				returnErr = err
			}
		}

		close(c.quit)
		c.log.Debugf("Connection closed")
	})

	return returnErr
}

// Done returns the quit channel of the ServerConn and thus can be used
// to determine if the current connection is closed or not.
func (c *ServerConn) Done() <-chan struct{} {
	return c.quit
}

// setStatus is used to set a new connection status and to call the onNewStatus
// callback function.
func (c *ServerConn) setStatus(s ServerStatus) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()

	if c.onNewStatus != nil && c.status != s {
		c.onNewStatus(s)
	}

	c.status = s
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
