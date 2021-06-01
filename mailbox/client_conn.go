package mailbox

import (
	"context"
	"fmt"
	"net"
	"regexp"

	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/lightninglabs/terminal-connect/hashmailrpc"
	"nhooyr.io/websocket"
)

var (
	receivePath = "/v1/terminal-connect/hashmail/receive"
	sendPath    = "/v1/terminal-connect/hashmail/send"
	addrFormat  = "wss://%s%s?method=POST"

	resultPattern    = regexp.MustCompile("{\"result\":(.*)}")
	defaultMarshaler = &runtime.JSONPb{OrigName: true, EmitDefaults: false}
)

// ClientConn is a type that establishes a base transport connection to a
// mailbox server using a REST/WebSocket connection. This type can be used to
// initiate a mailbox transport connection from a browser/WASM environment.
type ClientConn struct {
	*connKit

	receiveSocket *websocket.Conn
	sendSocket    *websocket.Conn

	receiveInitialized bool
}

// NewClientConn creates a new client connection with the given receive and send
// session identifiers. The context given as the first parameter will be used
// throughout the connection lifetime.
func NewClientConn(ctx context.Context, receiveSID,
	sendSID [64]byte) *ClientConn {

	c := &ClientConn{}
	c.connKit = &connKit{
		ctx:        ctx,
		impl:       c,
		receiveSID: receiveSID,
		sendSID:    sendSID,
	}

	return c
}

// Dial returns a net.Conn abstraction over the mailbox connection.
func (c *ClientConn) Dial(_ context.Context, serverHost string) (net.Conn,
	error) {

	c.connKit.serverAddr = serverHost

	connectMsg := NewMsgConnect(ProtocolVersion)
	return c, c.SendControlMsg(connectMsg)
}

// ReceiveControlMsg tries to receive a control message over the underlying
// mailbox connection.
//
// NOTE: This is part of the Conn interface.
func (c *ClientConn) ReceiveControlMsg(receive ControlMsg) error {
	if c.receiveSocket == nil {
		receiveAddr := fmt.Sprintf(
			addrFormat, c.serverAddr, receivePath,
		)
		receiveSocket, _, err := websocket.Dial(c.ctx, receiveAddr, nil)
		if err != nil {
			return err
		}
		c.receiveSocket = receiveSocket
	}

	if !c.receiveInitialized {
		receiveInit := &hashmailrpc.CipherBoxDesc{
			StreamId: c.receiveSID[:],
		}
		receiveInitBytes, err := defaultMarshaler.Marshal(receiveInit)
		if err != nil {
			return err
		}

		err = c.receiveSocket.Write(
			c.ctx, websocket.MessageText, receiveInitBytes,
		)
		if err != nil {
			return err
		}

		c.receiveInitialized = true
	}

	_, msg, err := c.receiveSocket.Read(c.ctx)
	if err != nil {
		return err
	}
	unwrapped := stripJSONWrapper(string(msg))

	mailboxMsg := &hashmailrpc.CipherBox{}
	err = defaultMarshaler.Unmarshal([]byte(unwrapped), mailboxMsg)
	if err != nil {
		return err
	}

	return receive.Deserialize(mailboxMsg.Msg)
}

// SendControlMsg tries to send a control message over the underlying mailbox
// connection.
//
// NOTE: This is part of the Conn interface.
func (c *ClientConn) SendControlMsg(controlMsg ControlMsg) error {
	if c.sendSocket == nil {
		sendAddr := fmt.Sprintf(addrFormat, c.serverAddr, sendPath)
		sendSocket, _, err := websocket.Dial(c.ctx, sendAddr, nil)
		if err != nil {
			return err
		}

		c.sendSocket = sendSocket
	}

	payloadBytes, err := controlMsg.Serialize()
	if err != nil {
		return err
	}

	sendInit := &hashmailrpc.CipherBox{
		Desc: &hashmailrpc.CipherBoxDesc{
			StreamId: c.sendSID[:],
		},
		Msg: payloadBytes,
	}
	sendInitBytes, err := defaultMarshaler.Marshal(sendInit)
	if err != nil {
		return err
	}
	return c.sendSocket.Write(c.ctx, websocket.MessageText, sendInitBytes)
}

// Close closes the underlying mailbox connection.
//
// NOTE: This is part of the net.Conn interface.
func (c *ClientConn) Close() error {
	var returnErr error
	if c.receiveSocket != nil {
		returnErr = c.receiveSocket.Close(
			websocket.StatusGoingAway, "bye",
		)
	}
	if c.sendSocket != nil {
		returnErr = c.sendSocket.Close(
			websocket.StatusGoingAway, "bye",
		)
	}

	return returnErr
}

var _ Conn = (*ClientConn)(nil)

func stripJSONWrapper(wrapped string) string {
	return resultPattern.ReplaceAllString(wrapped, "${1}")
}
