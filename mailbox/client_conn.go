package mailbox

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/lightninglabs/lightning-node-connect/gbn"
	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"google.golang.org/protobuf/encoding/protojson"
	"nhooyr.io/websocket"
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

	// webSocketRecvLimit is used to set the websocket receive limit. The
	// default value of 32KB is enough due to the fact that grpc has a
	// default packet maximum of 32KB which we then further wrap in gbn and
	// hashmail messages.
	webSocketRecvLimit int64 = 100 * 1024 // 100KB

	// sendSocketTimeout is the timeout used for context cancellation on the
	// send socket.
	sendSocketTimeout = 1000 * time.Millisecond
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

	receiveSocket *websocket.Conn
	receiveMu     sync.Mutex

	sendSocket *websocket.Conn
	sendMu     sync.Mutex

	gbnConn    *gbn.GoBackNConn
	gbnOptions []gbn.Option

	status      ClientStatus
	onNewStatus func(status ClientStatus)
	statusMu    sync.Mutex

	quit      chan struct{}
	cancel    func()
	closeOnce sync.Once
}

// NewClientConn creates a new client connection with the given receive and send
// session identifiers. The context given as the first parameter will be used
// throughout the connection lifetime.
func NewClientConn(ctx context.Context, sid [64]byte, serverHost string,
	onNewStatus func(status ClientStatus)) (*ClientConn, error) {

	receiveSID := GetSID(sid, true)
	sendSID := GetSID(sid, false)

	log.Debugf("New client conn, read_stream=%x, write_stream=%x",
		receiveSID[:], sendSID[:])

	ctxc, cancel := context.WithCancel(ctx)
	c := &ClientConn{
		gbnOptions: []gbn.Option{
			gbn.WithTimeout(gbnTimeout),
			gbn.WithHandshakeTimeout(gbnHandshakeTimeout),
			gbn.WithKeepalivePing(
				gbnClientPingTimeout, gbnPongTimeout,
			),
		},
		status:      ClientStatusNotConnected,
		onNewStatus: onNewStatus,
		quit:        make(chan struct{}),
		cancel:      cancel,
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

	log.Debugf("Refreshing client conn, read_stream=%x, write_stream=%x",
		c.receiveSID[:], c.sendSID[:])

	cc := &ClientConn{
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
	if c.receiveSocket == nil {
		c.createReceiveMailBox(ctx, 0)
	}
	c.receiveMu.Unlock()

	for {
		select {
		case <-c.quit:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		c.receiveMu.Lock()
		_, msg, err := c.receiveSocket.Read(ctx)
		if err != nil {
			log.Debugf("Client: got failure on receive socket, "+
				"re-trying: %v", err)

			c.setStatus(ClientStatusNotConnected)
			c.createReceiveMailBox(ctx, retryWait)
			c.receiveMu.Unlock()
			continue
		}
		unwrapped, err := stripJSONWrapper(string(msg))
		if err != nil {
			log.Debugf("Client: got error message from receive "+
				"socket: %v", err)

			c.setStatus(statusFromError(err))
			c.createReceiveMailBox(ctx, retryWait)
			c.receiveMu.Unlock()
			continue
		}
		c.receiveMu.Unlock()

		mailboxMsg := &hashmailrpc.CipherBox{}
		err = defaultMarshaler.Unmarshal([]byte(unwrapped), mailboxMsg)
		if err != nil {
			return nil, err
		}

		c.setStatus(ClientStatusConnected)

		return mailboxMsg.Msg, nil
	}
}

// send is used to send a payload on the send socket. The function is passed to
// and used by the gbn connection. It therefore takes in and reacts on the
// cancellation of a context so that the gbn connection is able to close
// independently of the ClientConn.
func (c *ClientConn) send(ctx context.Context, payload []byte) error {
	// Set up the send-socket if it has not yet been initialized.
	c.sendMu.Lock()
	if c.sendSocket == nil {
		c.createSendMailBox(ctx, 0)
	}
	c.sendMu.Unlock()

	// Retry sending the payload to the hashmail server until it succeeds.
	for {
		select {
		case <-c.quit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		sendInit := &hashmailrpc.CipherBox{
			Desc: &hashmailrpc.CipherBoxDesc{
				StreamId: c.sendSID[:],
			},
			Msg: payload,
		}

		sendInitBytes, err := defaultMarshaler.Marshal(sendInit)
		if err != nil {
			return err
		}

		c.sendMu.Lock()
		ctxt, cancel := context.WithTimeout(ctx, sendSocketTimeout)
		err = c.sendSocket.Write(
			ctxt, websocket.MessageText, sendInitBytes,
		)
		cancel()
		if err != nil {
			log.Debugf("Client: got failure on send socket, "+
				"re-trying: %v", err)

			c.setStatus(statusFromError(err))
			c.createSendMailBox(ctx, retryWait)
			c.sendMu.Unlock()
			continue
		}
		c.sendMu.Unlock()

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

		receiveAddr := fmt.Sprintf(
			addrFormat, c.serverAddr, receivePath,
		)
		receiveSocket, _, err := websocket.Dial(ctx, receiveAddr, nil)
		if err != nil {
			log.Debugf("Client: error creating receive socket %v",
				err)

			continue
		}
		receiveSocket.SetReadLimit(webSocketRecvLimit)
		c.receiveSocket = receiveSocket

		receiveInit := &hashmailrpc.CipherBoxDesc{
			StreamId: c.receiveSID[:],
		}
		receiveInitBytes, err := defaultMarshaler.Marshal(receiveInit)
		if err != nil {
			log.Debugf("Client: error marshaling receive init "+
				"bytes %w", err)

			continue
		}

		ctxt, cancel := context.WithTimeout(ctx, sendSocketTimeout)
		err = c.receiveSocket.Write(
			ctxt, websocket.MessageText, receiveInitBytes,
		)
		cancel()
		if err != nil {
			log.Debugf("Client: error creating receive stream "+
				"%v", err)

			continue
		}

		log.Debugf("Client: receive mailbox initialized")
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

		log.Debugf("Client: Attempting to create send socket")
		sendAddr := fmt.Sprintf(addrFormat, c.serverAddr, sendPath)
		sendSocket, _, err := websocket.Dial(ctx, sendAddr, nil)
		if err != nil {
			log.Debugf("Client: error creating send socket %v", err)
			continue
		}

		c.sendSocket = sendSocket

		log.Debugf("Client: Send socket created")
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
		log.Debugf("Closing client connection")

		if c.gbnConn != nil {
			if err := c.gbnConn.Close(); err != nil {
				log.Debugf("Error closing gbn connection: %v",
					err)

				returnErr = err
			}
		}

		c.receiveMu.Lock()
		if c.receiveSocket != nil {
			log.Debugf("sending bye on receive socket")
			err := c.receiveSocket.Close(
				websocket.StatusNormalClosure, "bye",
			)
			if err != nil {
				log.Errorf("Error closing receive socket: %v",
					err)

				returnErr = err
			}
		}
		c.receiveMu.Unlock()

		c.sendMu.Lock()
		if c.sendSocket != nil {
			log.Debugf("sending bye on send socket")
			err := c.sendSocket.Close(
				websocket.StatusNormalClosure, "bye",
			)
			if err != nil {
				log.Errorf("Error closing send socket: %v", err)

				returnErr = err
			}
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
		return "", fmt.Errorf(errMsg)
	}

	return "", fmt.Errorf("unrecognized JSON message: %v", wrapped)
}
