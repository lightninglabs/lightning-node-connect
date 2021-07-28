package mailbox

import (
	"context"
	"fmt"

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

	receiveBoxCreated bool
	receiveStream     hashmailrpc.HashMail_RecvStreamClient

	sendBoxCreated bool
	sendStream     hashmailrpc.HashMail_SendStreamClient
}

// NewServerConn creates a new net.Conn compatible server connection that uses
// a gRPC based connection to tunnel traffic over a mailbox server.
func NewServerConn(ctx context.Context, serverHost string,
	client hashmailrpc.HashMailClient, receiveSID,
	sendSID [64]byte) *ServerConn {

	c := &ServerConn{
		client: client,
	}
	c.connKit = &connKit{
		ctx:        ctx,
		serverAddr: serverHost,
		impl:       c,
		receiveSID: receiveSID,
		sendSID:    sendSID,
	}

	return c
}

// ReceiveControlMsg tries to receive a control message over the underlying
// mailbox connection.
//
// NOTE: This is part of the Conn interface.
func (c *ServerConn) ReceiveControlMsg(receive ControlMsg) error {
	if !c.receiveBoxCreated {
		err := initAccountCipherBox(c.ctx, c.client, c.receiveSID)
		if err != nil && !isErrAlreadyExists(err) {
			return fmt.Errorf("error creating read cipher box: %v",
				err)
		}

		c.receiveBoxCreated = true
	}

	if c.receiveStream == nil {
		streamDesc := &hashmailrpc.CipherBoxDesc{
			StreamId: c.receiveSID[:],
		}
		readStream, err := c.client.RecvStream(c.ctx, streamDesc)
		if err != nil {
			return fmt.Errorf("unable to create read stream: %w",
				err)
		}

		c.receiveStream = readStream
	}

	controlMsg, err := c.receiveStream.Recv()
	if err != nil {
		return fmt.Errorf("error reading control message from cipher "+
			"box: %v", err)
	}

	if err := receive.Deserialize(controlMsg.Msg); err != nil {
		return fmt.Errorf("error parsing control message: %v", err)
	}

	return nil
}

// SendControlMsg tries to send a control message over the underlying mailbox
// connection.
//
// NOTE: This is part of the Conn interface.
func (c *ServerConn) SendControlMsg(controlMsg ControlMsg) error {
	if !c.sendBoxCreated {
		err := initAccountCipherBox(c.ctx, c.client, c.sendSID)
		if err != nil && !isErrAlreadyExists(err) {
			return fmt.Errorf("error creating send cipher box: %v",
				err)
		}

		c.sendBoxCreated = true
	}

	payload, err := controlMsg.Serialize()
	if err != nil {
		return fmt.Errorf("error serializing message: %v", err)
	}

	if c.sendStream == nil {
		writeStream, err := c.client.SendStream(c.ctx)
		if err != nil {
			return fmt.Errorf("unable to create send stream: %w",
				err)
		}

		c.sendStream = writeStream
	}

	err = c.sendStream.Send(&hashmailrpc.CipherBox{
		Desc: &hashmailrpc.CipherBoxDesc{
			StreamId: c.sendSID[:],
		},
		Msg: payload,
	})
	if err != nil {
		return fmt.Errorf("error sending control message: %v", err)
	}

	return nil
}

// Close closes the underlying mailbox connection and tries to clean up the
// streams that were created for this connection.
//
// NOTE: This is part of the net.Conn interface.
func (c *ServerConn) Close() error {
	var returnErr error
	if c.receiveStream != nil {
		if err := c.receiveStream.CloseSend(); err != nil {
			log.Errorf("error closing receive stream: %v", err)
			returnErr = err
		}
	}

	if c.receiveBoxCreated {
		if err := delCipherBox(c.ctx, c.client, c.receiveSID); err != nil {
			log.Errorf("error removing receive cipher box: %v", err)
			returnErr = err
		}
	}

	if c.sendStream != nil {
		if err := c.sendStream.CloseSend(); err != nil {
			log.Errorf("error closing send stream: %v", err)
			returnErr = err
		}
	}

	if c.sendBoxCreated {
		if err := delCipherBox(c.ctx, c.client, c.sendSID); err != nil {
			log.Errorf("error removing send cipher box: %v", err)
			returnErr = err
		}
	}

	return returnErr
}

var _ Conn = (*ServerConn)(nil)

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
