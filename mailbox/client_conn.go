package mailbox

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sync"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/lightninglabs/terminal-connect/gbn"
	"github.com/lightninglabs/terminal-connect/hashmailrpc"
	"google.golang.org/protobuf/encoding/protojson"
	"nhooyr.io/websocket"
)

var (
	receivePath = "/v1/terminal-connect/hashmail/receive"
	sendPath    = "/v1/terminal-connect/hashmail/send"
	addrFormat  = "wss://%s%s?method=POST"

	resultPattern    = regexp.MustCompile("{\"result\":(.*)}")
	errorPattern     = regexp.MustCompile("{\"error\":(.*)}")
	defaultMarshaler = &runtime.JSONPb{
		MarshalOptions: protojson.MarshalOptions{
			UseProtoNames:   true,
			EmitUnpopulated: true,
		},
	}

	retryWait        = 2000 * time.Millisecond
	gbnTimeout       = 1000 * time.Millisecond
	gbnN       uint8 = 100

	webSocketRecvLimit int64 = 100 * 1024 // 100KB
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

	gbnConn *gbn.GoBackNConn

	closeOnce sync.Once

	quit chan struct{}
}

// NewClientConn creates a new client connection with the given receive and send
// session identifiers. The context given as the first parameter will be used
// throughout the connection lifetime.
func NewClientConn(ctx context.Context, receiveSID,
	sendSID [64]byte) *ClientConn {

	log.Debugf("New client conn, read_stream=%x, write_stream=%x",
		receiveSID[:], sendSID[:])

	c := &ClientConn{
		quit: make(chan struct{}),
	}
	c.connKit = &connKit{
		ctx:        ctx,
		impl:       c,
		receiveSID: receiveSID,
		sendSID:    sendSID,
	}
	return c
}

func (c *ClientConn) recvFromStream(ctx context.Context) ([]byte, error) {
	c.receiveStreamMu.Lock()
	if c.receiveSocket == nil {
		c.createReceiveMailBox(ctx)
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

			time.Sleep(retryWait)
			c.createReceiveMailBox(ctx)
			c.receiveStreamMu.Unlock()

			continue
		}
		unwrapped, err := stripJSONWrapper(string(msg))
		if err != nil {
			log.Debugf("Client: got error message from receive "+
				"socket: %v", err)

			time.Sleep(retryWait)
			c.createReceiveMailBox(ctx)
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

func (c *ClientConn) sendToStream(ctx context.Context, payload []byte) error {
	c.sendStreamMu.Lock()
	if c.sendSocket == nil {
		c.createSendMailBox(ctx)
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
		ctxt, _ := context.WithTimeout(c.ctx, time.Second)
		err = c.sendSocket.Write(ctxt, websocket.MessageText, sendInitBytes)
		if err != nil {
			log.Debugf("Client: got failure on send socket, "+
				"re-trying: %v", err)

			time.Sleep(retryWait)
			c.createSendMailBox(ctx)
			c.sendStreamMu.Unlock()

			continue
		}
		c.sendStreamMu.Unlock()

		return nil
	}
}

func (c *ClientConn) createReceiveMailBox(ctx context.Context) {
	var delay time.Duration
	for {
		select {
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		default:
		}

		time.Sleep(delay)
		delay = retryWait

		receiveAddr := fmt.Sprintf(
			addrFormat, c.serverAddr, receivePath,
		)
		receiveSocket, _, err := websocket.Dial(ctx, receiveAddr, nil)
		if err != nil {
			log.Debugf("Client: error creating receive socket %w", err)
			continue
		}
		receiveSocket.SetReadLimit(webSocketRecvLimit)
		c.receiveSocket = receiveSocket

		receiveInit := &hashmailrpc.CipherBoxDesc{
			StreamId: c.receiveSID[:],
		}
		receiveInitBytes, err := defaultMarshaler.Marshal(receiveInit)
		if err != nil {
			log.Debugf("Client: error marshaling receive init bytes %w", err)
			continue
		}

		err = c.receiveSocket.Write(
			ctx, websocket.MessageText, receiveInitBytes,
		)
		if err != nil {
			log.Debugf("Client: error creating receive stream %w", err)
			continue
		}

		log.Debugf("Client: receive mailbox")
		return
	}
}

func (c *ClientConn) createSendMailBox(ctx context.Context) {
	var delay time.Duration
	for {
		select {
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		default:
		}

		time.Sleep(delay)
		delay = retryWait

		sendAddr := fmt.Sprintf(addrFormat, c.serverAddr, sendPath)
		sendSocket, _, err := websocket.Dial(ctx, sendAddr, nil)
		if err != nil {
			log.Debugf("Client: error creating send socket %w", err)
			continue
		}

		c.sendSocket = sendSocket

		log.Debugf("Client: Send mailbox")
		return
	}
}

// Dial returns a net.Conn abstraction over the mailbox connection.
func (c *ClientConn) Dial(_ context.Context, serverHost string) (net.Conn,
	error) {

	c.connKit.serverAddr = serverHost

	gbnConn, err := gbn.NewClientConn(
		gbnN, c.sendToStream, c.recvFromStream, gbnTimeout,
	)
	if err != nil {
		return nil, err
	}
	c.gbnConn = gbnConn
	c.quit = make(chan struct{})

	return c, nil
}

// ReceiveControlMsg tries to receive a control message over the underlying
// mailbox connection.
//
// NOTE: This is part of the Conn interface.
func (c *ClientConn) ReceiveControlMsg(receive ControlMsg) error {
	log.Debugf("Client: waiting for %T", receive)
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
	log.Debugf("Client: sending %T", controlMsg)
	return c.gbnConn.Send(payloadBytes)
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

		close(c.quit)

		if c.receiveSocket != nil {
			log.Debugf("sending bye on receive socket")
			returnErr = c.receiveSocket.Close(
				websocket.StatusGoingAway, "bye",
			)
		}

		if c.sendSocket != nil {
			log.Debugf("sending bye on send socket")
			returnErr = c.sendSocket.Close(
				websocket.StatusGoingAway, "bye",
			)
		}
	})

	return returnErr
}

var _ Conn = (*ClientConn)(nil)

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
