package mailbox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btclog/v2"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/lightninglabs/lightning-node-connect/gbn"
	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	// receivePath is the URL under which the read stream of the mailbox
	// server's WebSocket proxy is reachable. We keep this under the old
	// name to make the version backward compatible with the closed beta.
	receivePath = "/v1/lightning-node-connect/hashmail/receive"

	// sendPath is the URL under which the write stream of the mailbox
	// server's WebSocket proxy is reachable. We keep this under the old
	// name to make the version backward compatible with the closed beta.
	sendPath   = "/v1/lightning-node-connect/hashmail/send"
	addrFormat = "wss://%s%s?method=POST"

	resultPattern    = regexp.MustCompile("{\"result\":(.*)}")
	errorPattern     = regexp.MustCompile("{\"error\":(.*)}")
	defaultMarshaler = &runtime.JSONPb{
		MarshalOptions: protojson.MarshalOptions{
			UseProtoNames:   true,
			EmitUnpopulated: true,
		},
	}
)

const (
	// retryWait is the duration that we will wait before retrying to
	// connect to the hashmail server if a connection error occurred.
	retryWait = 2000 * time.Millisecond

	// gbnTimeout is the timeout that we want the gbn connection to wait
	// to receive ACKS from the peer before resending the queue.
	gbnTimeout = 1000 * time.Millisecond

	// gbnResendMultiplier is the multiplier that we want the gbn
	// connection to use when dynamically setting the resend timeout.
	gbnResendMultiplier = 5

	// gbnTimeoutUpdateFrequency is the frequency representing the number of
	// packages + responses we want, before we update the resend timeout.
	gbnTimeoutUpdateFrequency = 200

	// gbnN is the queue size, N, that the gbn server will use. The gbn
	// server will send up to N packets before requiring an ACK for the
	// first packet in the queue.
	gbnN uint8 = gbn.DefaultN

	// gbnHandshakeTimeout is the time after which the gbn connection
	// will abort and restart the handshake after not receiving a response
	// from the peer. This timeout needs to be long enough for the server to
	// set up the clients send stream cipher box.
	gbnHandshakeTimeout = 2000 * time.Millisecond

	// gbnClientPingTimeout is the time after with the client will send the
	// server a ping message if it has not received any packets from the
	// server. The client will close the connection if it then does not
	// receive an acknowledgement of the ping from the server.
	gbnClientPingTimeout = 7 * time.Second

	// gbnServerTimeout is the time after with the server will send the
	// client a ping message if it has not received any packets from the
	// client. The server will close the connection if it then does not
	// receive an acknowledgement of the ping from the client. This timeout
	// is slightly shorter than the gbnClientPingTimeout to prevent both
	// sides from unnecessarily sending pings simultaneously.
	gbnServerPingTimeout = 5 * time.Second

	// gbnPongTimout is the time after sending the pong message that we will
	// timeout if we do not receive any message from our peer.
	gbnPongTimeout = 3 * time.Second

	// gbnBoostPercent is the percentage value that the resend and handshake
	// timeout will be boosted any time we need to resend a packet due to
	// the corresponding response not being received within the previous
	// timeout.
	gbnBoostPercent = 0.5
)

// ClientStatus is a description of the connection status of the client.
type ClientStatus string

const (
	// ClientStatusNotConnected means that the client is not connected at
	// all. This is likely due to the mailbox server being down.
	ClientStatusNotConnected ClientStatus = "Not Connected"

	// ClientStatusSessionNotFound means that the connection to the mailbox
	// server was successful but that the mailbox with the given ID was not
	// found. This either means that the server is down, that the session
	// has expired or that the user entered passphrase was incorrect.
	ClientStatusSessionNotFound ClientStatus = "Session Not Found"

	// ClientStatusSessionInUse means that the connection to the mailbox
	// server was successful but that the stream for the session is already
	// occupied by another client.
	ClientStatusSessionInUse ClientStatus = "Session In Use"

	// ClientStatusConnected indicates that the connection to both the
	// mailbox server and end server was successful.
	ClientStatusConnected ClientStatus = "Connected"
)

// String converts the ClientStatus to a string type.
func (c ClientStatus) String() string {
	return string(c)
}

// ClientConn is a type that establishes a base transport connection to a
// mailbox server using a REST/WebSocket connection. This type can be used to
// initiate a mailbox transport connection from a browser/WASM environment.
type ClientConn struct {
	*connKit

	transport ClientConnTransport
	receiveMu sync.Mutex
	sendMu    sync.Mutex

	gbnConn    *gbn.GoBackNConn
	gbnOptions []gbn.Option

	status      ClientStatus
	onNewStatus func(status ClientStatus)
	statusMu    sync.Mutex

	quit      chan struct{}
	cancel    func()
	closeOnce sync.Once

	log btclog.Logger
}

// NewClientConn creates a new client connection with the given receive and send
// session identifiers. The context given as the first parameter will be used
// throughout the connection lifetime.
func NewClientConn(ctx context.Context, sid [64]byte, serverHost string,
	client hashmailrpc.HashMailClient, logger btclog.Logger,
	onNewStatus func(status ClientStatus)) (*ClientConn, error) {

	receiveSID := GetSID(sid, true)
	sendSID := GetSID(sid, false)
	mailBoxInfo := &mailboxInfo{
		addr:    serverHost,
		recvSID: receiveSID[:],
		sendSID: sendSID[:],
	}

	logger.Debugf("New conn, read_stream=%x, write_stream=%x",
		receiveSID[:], sendSID[:])

	ctxc, cancel := context.WithCancel(ctx)

	var transport ClientConnTransport
	if client != nil {
		transport = newGrpcTransport(mailBoxInfo, client)
	} else {
		transport = newWebsocketTransport(mailBoxInfo)
	}

	c := &ClientConn{
		transport:   transport,
		status:      ClientStatusNotConnected,
		onNewStatus: onNewStatus,
		quit:        make(chan struct{}),
		cancel:      cancel,
		log:         logger,
	}

	c.gbnOptions = []gbn.Option{
		gbn.WithTimeoutOptions(
			gbn.WithResendMultiplier(gbnResendMultiplier),
			gbn.WithTimeoutUpdateFrequency(
				gbnTimeoutUpdateFrequency,
			),
			gbn.WithHandshakeTimeout(gbnHandshakeTimeout),
			gbn.WithKeepalivePing(
				gbnClientPingTimeout, gbnPongTimeout,
			),
			gbn.WithBoostPercent(gbnBoostPercent),
		),
		gbn.WithOnFIN(func() {
			// We force the connection to set a new status after
			// processing a FIN packet, as in rare occasions the
			// corresponding server may have time to close the
			// connection before we've already processed the sent
			// FIN packet by the server. In that case, if we didn't
			// force a new status, the client would never mark the
			// connection as status ClientStatusSessionNotFound.
			c.setStatus(ClientStatusSessionNotFound)
		}),
	}

	c.connKit = &connKit{
		ctx:        ctxc,
		impl:       c,
		receiveSID: receiveSID,
		sendSID:    sendSID,
		serverAddr: serverHost,
	}

	gbnConn, err := gbn.NewClientConn(
		ctxc, gbnN, c.send, c.recv, c.gbnOptions...,
	)
	if err != nil {
		return nil, err
	}
	c.gbnConn = gbnConn

	return c, nil
}

// RefreshClientConn creates a new ClientConn object with the same values as
// the passed ClientConn but with a new quit channel, a new closeOnce var and
// a new gbn connection.
func RefreshClientConn(ctx context.Context, c *ClientConn) (*ClientConn,
	error) {

	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	c.receiveMu.Lock()
	defer c.receiveMu.Unlock()

	c.statusMu.Lock()
	defer c.statusMu.Unlock()

	c.log.Debugf("Refreshing client conn, read_stream=%x, write_stream=%x",
		c.receiveSID[:], c.sendSID[:])

	cc := &ClientConn{
		log:         c.log,
		transport:   c.transport.Refresh(),
		status:      ClientStatusNotConnected,
		onNewStatus: c.onNewStatus,
		gbnOptions:  c.gbnOptions,
		cancel:      c.cancel,
		quit:        make(chan struct{}),
	}

	cc.connKit = &connKit{
		ctx:        ctx,
		impl:       cc,
		receiveSID: c.receiveSID,
		sendSID:    c.sendSID,
		serverAddr: c.serverAddr,
	}

	gbnConn, err := gbn.NewClientConn(
		ctx, gbnN, cc.send, cc.recv, cc.gbnOptions...,
	)
	if err != nil {
		return nil, err
	}
	cc.gbnConn = gbnConn

	return cc, nil
}

// Done returns the quit channel of the ClientConn and thus can be used
// to determine if the current connection is closed or not.
func (c *ClientConn) Done() <-chan struct{} {
	return c.quit
}

// setStatus is used to set a new connection status and to call the onNewStatus
// callback function.
func (c *ClientConn) setStatus(s ClientStatus) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()

	// We give previously set status priority if it provides more detail
	// than the NotConnected status.
	if s == ClientStatusNotConnected &&
		(c.status == ClientStatusSessionInUse ||
			c.status == ClientStatusSessionNotFound) {

		return
	}

	c.status = s
	c.onNewStatus(s)
}

// statusFromError parses the given error and returns an appropriate Client
// connection status.
func statusFromError(err error) ClientStatus {
	switch {
	case strings.Contains(err.Error(), "stream not found"):
		return ClientStatusSessionNotFound

	case strings.Contains(err.Error(), "stream occupied"):
		return ClientStatusSessionInUse

	default:
		return ClientStatusNotConnected
	}
}

// recv is used to receive a payload from the receive socket. The function is
// passed to and used by the gbn connection. It therefore takes in and reacts
// on the cancellation of a context so that the gbn connection is able to close
// independently of the ClientConn.
func (c *ClientConn) recv(ctx context.Context) ([]byte, error) {
	c.receiveMu.Lock()
	defer c.receiveMu.Unlock()

	if !c.transport.ReceiveConnected() {
		c.createReceiveMailBox(ctx, 0)
	}

	for {
		select {
		case <-c.quit:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		msg, retry, errStatus, err := c.transport.Recv(ctx)
		if err != nil {
			if !retry {
				return nil, err
			}

			c.log.Debugf("Got failure on receive "+
				"socket/stream, re-trying: %v", err)

			c.setStatus(errStatus)
			c.createReceiveMailBox(ctx, retryWait)
			continue
		}

		c.setStatus(ClientStatusConnected)
		return msg, nil
	}
}

// send is used to send a payload on the send socket. The function is passed to
// and used by the gbn connection. It therefore takes in and reacts on the
// cancellation of a context so that the gbn connection is able to close
// independently of the ClientConn.
func (c *ClientConn) send(ctx context.Context, payload []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	// Set up the send-socket if it has not yet been initialized.
	if !c.transport.SendConnected() {
		c.createSendMailBox(ctx, 0)
	}

	// Retry sending the payload to the hashmail server until it succeeds.
	for {
		select {
		case <-c.quit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		retry, errStatus, err := c.transport.Send(
			ctx, c.sendSID[:], payload,
		)
		if err != nil {
			if !retry {
				return err
			}

			c.log.Debugf("Got failure on send socket/stream, "+
				"re-trying: %v", err)

			c.setStatus(errStatus)
			c.createSendMailBox(ctx, retryWait)
			continue
		}

		return nil
	}
}

// createReceiveMailBox attempts to connect to the hashmail server and
// initialize a read stream for the given mailbox ID. It retries if any errors
// occur.
// TODO(elle): maybe have a max number of retries and close the connection if
// that maximum is exceeded.
func (c *ClientConn) createReceiveMailBox(ctx context.Context,
	initialBackoff time.Duration) {

	waiter := gbn.NewBackoffWaiter(initialBackoff, retryWait, retryWait)

	for {
		select {
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		default:
		}

		waiter.Wait()

		if err := c.transport.ConnectReceive(ctx); err != nil {
			c.log.Errorf("Error connecting to receive "+
				"socket/stream: %v", err)

			continue
		}

		c.log.Debugf("Receive mailbox initialized")

		return
	}
}

// createSendMailBox attempts to open a websocket to the hashmail server that
// will be used to send packets on.
func (c *ClientConn) createSendMailBox(ctx context.Context,
	initialBackoff time.Duration) {

	waiter := gbn.NewBackoffWaiter(initialBackoff, retryWait, retryWait)

	for {
		select {
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		default:
		}

		waiter.Wait()

		c.log.Debugf("Attempting to create send socket/stream")

		if err := c.transport.ConnectSend(ctx); err != nil {
			c.log.Debugf("Error connecting to send "+
				"stream/socket %v", err)

			continue
		}

		c.log.Debugf("Connected to send socket/stream")

		return
	}
}

// ReceiveControlMsg tries to receive a control message over the underlying
// mailbox connection.
//
// NOTE: This is part of the Conn interface.
func (c *ClientConn) ReceiveControlMsg(receive ControlMsg) error {
	msg, err := c.gbnConn.Recv()
	if err != nil {
		return fmt.Errorf("error receiving from go-back-n "+
			"connection: %v", err)
	}

	return receive.Deserialize(msg)
}

// SendControlMsg tries to send a control message over the underlying mailbox
// connection.
//
// NOTE: This is part of the Conn interface.
func (c *ClientConn) SendControlMsg(controlMsg ControlMsg) error {
	payloadBytes, err := controlMsg.Serialize()
	if err != nil {
		return err
	}
	return c.gbnConn.Send(payloadBytes)
}

// SetRecvTimeout sets the timeout to be used when attempting to receive data.
func (c *ClientConn) SetRecvTimeout(timeout time.Duration) {
	c.gbnConn.SetRecvTimeout(timeout)
}

// SetSendTimeout sets the timeout to be used when attempting to send data.
func (c *ClientConn) SetSendTimeout(timeout time.Duration) {
	c.gbnConn.SetSendTimeout(timeout)
}

// Close closes the underlying mailbox connection.
//
// NOTE: This is part of the net.Conn interface.
func (c *ClientConn) Close() error {
	var returnErr error
	c.closeOnce.Do(func() {
		c.log.Debugf("Closing connection")

		if c.gbnConn != nil {
			if err := c.gbnConn.Close(); err != nil {
				c.log.Debugf("Error closing gbn connection: %v",
					err)

				returnErr = err
			}
		}

		c.receiveMu.Lock()
		c.log.Debugf("Closing receive stream/socket")
		if err := c.transport.CloseReceive(); err != nil {
			c.log.Errorf("Error closing receive stream/socket: %v",
				err)

			returnErr = err
		}
		c.receiveMu.Unlock()

		c.sendMu.Lock()
		c.log.Debugf("Closing send stream/socket")
		if err := c.transport.CloseSend(); err != nil {
			c.log.Errorf("Error closing send stream/socket: %v",
				err)

			returnErr = err
		}
		c.sendMu.Unlock()

		close(c.quit)
		c.cancel()
	})

	return returnErr
}

var _ ProxyConn = (*ClientConn)(nil)

func stripJSONWrapper(wrapped string) (string, error) {
	if resultPattern.MatchString(wrapped) {
		return resultPattern.ReplaceAllString(wrapped, "${1}"), nil
	}

	if errorPattern.MatchString(wrapped) {
		errMsg := errorPattern.ReplaceAllString(wrapped, "${1}")
		return "", errors.New(errMsg)
	}

	return "", fmt.Errorf("unrecognized JSON message: %v", wrapped)
}
