package mailbox

import (
	"context"
	"fmt"
	"regexp"
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

// ClientConn is a type that establishes a base transport connection to a
// mailbox server using a REST/WebSocket connection. This type can be used to
// initiate a mailbox transport connection from a browser/WASM environment.
type ClientConn struct {
	*connKit

	receiveSocket   *websocket.Conn
	receiveStreamMu sync.Mutex

	sendSocket   *websocket.Conn
	sendStreamMu sync.Mutex

	gbnConn    *gbn.GoBackNConn
	gbnOptions []gbn.Option

	closeOnce sync.Once

	quit chan struct{}
}

// NewClientConn creates a new client connection with the given receive and send
// session identifiers. The context given as the first parameter will be used
// throughout the connection lifetime.
func NewClientConn(ctx context.Context, sid [64]byte,
	serverHost string) (*ClientConn, error) {

	receiveSID := GetSID(sid, true)
	sendSID := GetSID(sid, false)

	log.Debugf("New client conn, read_stream=%x, write_stream=%x",
		receiveSID[:], sendSID[:])

	c := &ClientConn{
		gbnOptions: []gbn.Option{
			gbn.WithTimeout(gbnTimeout),
			gbn.WithHandshakeTimeout(gbnHandshakeTimeout),
			gbn.WithKeepalivePing(
				gbnClientPingTimeout, gbnPongTimeout,
			),
		},
		quit: make(chan struct{}),
	}
	c.connKit = &connKit{
		ctx:        ctx,
		impl:       c,
		receiveSID: receiveSID,
		sendSID:    sendSID,
		serverAddr: serverHost,
	}

	gbnConn, err := gbn.NewClientConn(
		gbnN, c.sendToStream, c.recvFromStream, c.gbnOptions...,
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
func RefreshClientConn(c *ClientConn) (*ClientConn, error) {
	c.sendStreamMu.Lock()
	defer c.sendStreamMu.Unlock()

	c.receiveStreamMu.Lock()
	defer c.receiveStreamMu.Unlock()

	log.Debugf("Refreshing client conn, read_stream=%x, write_stream=%x",
		c.receiveSID[:], c.sendSID[:])

	cc := &ClientConn{
		receiveSocket: c.receiveSocket,
		sendSocket:    c.sendSocket,
		gbnOptions:    c.gbnOptions,
		quit:          make(chan struct{}),
	}
	c.sendSocket = nil
	c.receiveSocket = nil

	cc.connKit = &connKit{
		ctx:        c.ctx,
		impl:       cc,
		receiveSID: c.receiveSID,
		sendSID:    c.sendSID,
		serverAddr: c.serverAddr,
	}

	gbnConn, err := gbn.NewClientConn(
		gbnN, cc.sendToStream, cc.recvFromStream, cc.gbnOptions...,
	)
	if err != nil {
		return cc, err
	}
	cc.gbnConn = gbnConn

	return cc, nil
}

// Done returns the quit channel of the ClientConn and thus can be used
// to determine if the current connection is closed or not.
func (c *ClientConn) Done() <-chan struct{} {
	return c.quit
}

// recvFromStream is used to receive a payload from the receive socket.
// The function is passed to and used by the gbn connection.
// It therefore takes in and reacts on the cancellation of a context so that
// the gbn connection is able to close independently of the ClientConn.
func (c *ClientConn) recvFromStream(ctx context.Context) ([]byte, error) {
	c.receiveStreamMu.Lock()
	if c.receiveSocket == nil {
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
		_, msg, err := c.receiveSocket.Read(ctx)
		if err != nil {
			log.Debugf("Client: got failure on receive socket, "+
				"re-trying: %v", err)

			c.createReceiveMailBox(ctx, retryWait)
			c.receiveStreamMu.Unlock()
			continue
		}
		unwrapped, err := stripJSONWrapper(string(msg))
		if err != nil {
			log.Debugf("Client: got error message from receive "+
				"socket: %v", err)

			c.createReceiveMailBox(ctx, retryWait)
			c.receiveStreamMu.Unlock()
			continue
		}
		c.receiveStreamMu.Unlock()

		mailboxMsg := &hashmailrpc.CipherBox{}
		err = defaultMarshaler.Unmarshal([]byte(unwrapped), mailboxMsg)
		if err != nil {
			return nil, err
		}

		return mailboxMsg.Msg, nil
	}
}

// sendToStream is used to send a payload on the send socket. The function
// is passed to and used by the gbn connection. It therefore takes in and
// reacts on the cancellation of a context so that the gbn connection is able to
// close independently of the ClientConn.
func (c *ClientConn) sendToStream(ctx context.Context, payload []byte) error {
	// Set up the send socket if it has not yet been initialized.
	c.sendStreamMu.Lock()
	if c.sendSocket == nil {
		c.createSendMailBox(ctx, 0)
	}
	c.sendStreamMu.Unlock()

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

		c.sendStreamMu.Lock()
		ctxt, cancel := context.WithTimeout(ctx, sendSocketTimeout)
		err = c.sendSocket.Write(
			ctxt, websocket.MessageText, sendInitBytes,
		)
		cancel()
		if err != nil {
			log.Debugf("Client: got failure on send socket, "+
				"re-trying: %v", err)

			c.createSendMailBox(ctx, retryWait)
			c.sendStreamMu.Unlock()
			continue
		}
		c.sendStreamMu.Unlock()

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

		if err := c.gbnConn.Close(); err != nil {
			log.Debugf("Error closing gbn connection: %v", err)
		}

		c.receiveStreamMu.Lock()
		if c.receiveSocket != nil {
			log.Debugf("sending bye on receive socket")
			returnErr = c.receiveSocket.Close(
				websocket.StatusGoingAway, "bye",
			)
		}
		c.receiveStreamMu.Unlock()

		c.sendStreamMu.Lock()
		if c.sendSocket != nil {
			log.Debugf("sending bye on send socket")
			returnErr = c.sendSocket.Close(
				websocket.StatusGoingAway, "bye",
			)
		}
		c.sendStreamMu.Unlock()

		close(c.quit)
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
