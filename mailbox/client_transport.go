package mailbox

import (
	"context"
	"fmt"
	"time"

	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"nhooyr.io/websocket"
)

const (
	// webSocketRecvLimit is used to set the websocket receive limit. The
	// default value of 32KB is enough due to the fact that grpc has a
	// default packet maximum of 32KB which we then further wrap in gbn and
	// hashmail messages.
	webSocketRecvLimit int64 = 100 * 1024 // 100KB

	// sendSocketTimeout is the timeout used for context cancellation on the
	// send socket.
	sendSocketTimeout = 1000 * time.Millisecond
)

// ClientConnTransport is an interface that hides the implementation of the
// underlying send and receive connections to a hashmail server.
type ClientConnTransport interface {
	// Refresh creates a new ClientConnTransport with no initialised send
	// or receive connections.
	Refresh() ClientConnTransport

	// ReceiveConnected returns true if the transport is connected to the
	// hashmail-server receive stream.
	ReceiveConnected() bool

	// SendConnected returns true if the transport is connected to the
	// hashmail-server send stream.
	SendConnected() bool

	// ConnectReceive can be called in order to initialise the
	// receive-stream with the hashmail-server.
	ConnectReceive(ctx context.Context) error

	// ConnectSend can be called in order to initialise the send-stream with
	// the hashmail-server.
	ConnectSend(ctx context.Context) error

	// Recv will attempt to read data off of the underlying transport's
	// receive stream.
	Recv(ctx context.Context) ([]byte, bool, ClientStatus, error)

	// Send will attempt to send data on the underlying transport's send
	// stream.
	Send(ctx context.Context, streamID, payload []byte) (bool, ClientStatus,
		error)

	// CloseReceive will close the transport's connection to the
	// receive-stream.
	CloseReceive() error

	// CloseSend will close the transport's connection to the send-stream.
	CloseSend() error
}

// mailboxInfo holds all the mailbox related info required in order to connect
// the correct read and write streams required to establish an LNC connection.
type mailboxInfo struct {
	addr    string
	recvSID []byte
	sendSID []byte
}

// websocketTransport is an implementation of ClientConnTransport that uses
// websocket connections to connect to the mailbox.
type websocketTransport struct {
	*mailboxInfo
	receiveSocket *websocket.Conn
	sendSocket    *websocket.Conn
}

// newWebsocketTransport constructs a new websocketTransport instance.
func newWebsocketTransport(mbInfo *mailboxInfo) *websocketTransport {
	return &websocketTransport{mailboxInfo: mbInfo}
}

// Refresh creates a new ClientConnTransport with no initialised send
// or receive connections.
//
// NOTE: this is part of the ClientConnTransport interface.
func (wt *websocketTransport) Refresh() ClientConnTransport {
	return &websocketTransport{
		mailboxInfo: wt.mailboxInfo,
	}
}

// ReceiveConnected returns true if the transport is connected to the
// hashmail-server receive stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (wt *websocketTransport) ReceiveConnected() bool {
	return wt.receiveSocket != nil
}

// SendConnected returns true if the transport is connected to the
// hashmail-server send stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (wt *websocketTransport) SendConnected() bool {
	return wt.sendSocket != nil
}

// ConnectSend can be called in order to initialise the send-stream with
// the hashmail-server.
//
// NOTE: this is part of the ClientConnTransport interface.
func (wt *websocketTransport) ConnectSend(ctx context.Context) error {
	sendAddr := fmt.Sprintf(addrFormat, wt.addr, sendPath)
	sendSocket, _, err := websocket.Dial(ctx, sendAddr, nil)
	if err != nil {
		return err
	}

	wt.sendSocket = sendSocket
	return nil
}

// ConnectReceive can be called in order to initialise the receive-stream with
// the hashmail-server.
//
// NOTE: this is part of the ClientConnTransport interface.
func (wt *websocketTransport) ConnectReceive(ctx context.Context) error {
	receiveAddr := fmt.Sprintf(addrFormat, wt.addr, receivePath)
	receiveSocket, _, err := websocket.Dial(ctx, receiveAddr, nil)
	if err != nil {
		return err
	}

	receiveSocket.SetReadLimit(webSocketRecvLimit)
	wt.receiveSocket = receiveSocket

	receiveInit := &hashmailrpc.CipherBoxDesc{StreamId: wt.recvSID}
	receiveInitBytes, err := defaultMarshaler.Marshal(receiveInit)
	if err != nil {
		return fmt.Errorf("error marshaling receive init bytes %v", err)
	}

	ctxt, cancel := context.WithTimeout(ctx, sendSocketTimeout)
	err = wt.receiveSocket.Write(
		ctxt, websocket.MessageText, receiveInitBytes,
	)
	cancel()
	if err != nil {
		return err
	}

	return nil
}

// Recv will attempt to read data off of the underlying transport's
// receive-stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (wt *websocketTransport) Recv(ctx context.Context) ([]byte, bool,
	ClientStatus, error) {

	_, msg, err := wt.receiveSocket.Read(ctx)
	if err != nil {
		return nil, true, ClientStatusNotConnected, err
	}

	unwrapped, err := stripJSONWrapper(string(msg))
	if err != nil {
		return nil, true, statusFromError(err), err
	}

	mailboxMsg := &hashmailrpc.CipherBox{}
	err = defaultMarshaler.Unmarshal([]byte(unwrapped), mailboxMsg)
	if err != nil {
		return nil, false, ClientStatusNotConnected, err
	}

	return mailboxMsg.Msg, false, ClientStatusConnected, nil
}

// Send will attempt to send data on the underlying transport's send-stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (wt *websocketTransport) Send(ctx context.Context, streamID,
	payload []byte) (bool, ClientStatus, error) {

	sendInit := &hashmailrpc.CipherBox{
		Desc: &hashmailrpc.CipherBoxDesc{
			StreamId: streamID,
		},
		Msg: payload,
	}

	sendInitBytes, err := defaultMarshaler.Marshal(sendInit)
	if err != nil {
		return false, ClientStatusNotConnected, err
	}

	ctxt, cancel := context.WithTimeout(ctx, sendSocketTimeout)
	err = wt.sendSocket.Write(ctxt, websocket.MessageText, sendInitBytes)
	cancel()
	if err != nil {
		return true, statusFromError(err), err
	}

	return false, ClientStatusConnected, nil
}

// CloseReceive will close the transport's connection to the receive-stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (wt *websocketTransport) CloseReceive() error {
	if wt.receiveSocket == nil {
		return nil
	}
	return wt.receiveSocket.Close(websocket.StatusNormalClosure, "bye")
}

// CloseSend will close the transport's connection to the send-stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (wt *websocketTransport) CloseSend() error {
	if wt.sendSocket == nil {
		return nil
	}

	return wt.sendSocket.Close(websocket.StatusNormalClosure, "bye")
}

// grpcTransport is an implementation of ClientConnTransport that uses grpc
// streams to connect the the mailbox.
type grpcTransport struct {
	*mailboxInfo

	client        hashmailrpc.HashMailClient
	receiveStream hashmailrpc.HashMail_RecvStreamClient
	sendStream    hashmailrpc.HashMail_SendStreamClient
}

// newGrpcTransport constructs a new grpcTransport instance.
func newGrpcTransport(mbInfo *mailboxInfo,
	client hashmailrpc.HashMailClient) *grpcTransport {

	return &grpcTransport{
		client:      client,
		mailboxInfo: mbInfo,
	}
}

// Refresh creates a new ClientConnTransport with no initialised send or receive
// connections.
//
// NOTE: this is part of the ClientConnTransport interface.
func (gt *grpcTransport) Refresh() ClientConnTransport {
	return &grpcTransport{
		client:      gt.client,
		mailboxInfo: gt.mailboxInfo,
	}
}

// ReceiveConnected returns true if the transport is connected to the
// hashmail-server receive stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (gt *grpcTransport) ReceiveConnected() bool {
	return gt.receiveStream != nil
}

// SendConnected returns true if the transport is connected to the
// hashmail-server send stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (gt *grpcTransport) SendConnected() bool {
	return gt.sendStream != nil
}

// ConnectSend can be called in order to initialise the send-stream with
// the hashmail-server.
//
// NOTE: this is part of the ClientConnTransport interface.
func (gt *grpcTransport) ConnectSend(ctx context.Context) error {
	sendStream, err := gt.client.SendStream(ctx)
	if err != nil {
		return err
	}

	gt.sendStream = sendStream
	return nil
}

// ConnectReceive can be called in order to initialise the receive-stream with
// the hashmail-server.
//
// NOTE: this is part of the ClientConnTransport interface.
func (gt *grpcTransport) ConnectReceive(ctx context.Context) error {
	receiveInit := &hashmailrpc.CipherBoxDesc{StreamId: gt.recvSID}
	readStream, err := gt.client.RecvStream(ctx, receiveInit)
	if err != nil {
		return err
	}

	gt.receiveStream = readStream
	return nil
}

// Recv will attempt to read data off of the underlying transport's
// receive stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (gt *grpcTransport) Recv(_ context.Context) ([]byte, bool, ClientStatus,
	error) {

	controlMsg, err := gt.receiveStream.Recv()
	if err != nil {
		return nil, true, statusFromError(err), err
	}

	return controlMsg.Msg, false, ClientStatusConnected, nil
}

// Send will attempt to send data on the underlying transport's send-stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (gt *grpcTransport) Send(_ context.Context, streamID, payload []byte) (
	bool, ClientStatus, error) {

	err := gt.sendStream.Send(&hashmailrpc.CipherBox{
		Desc: &hashmailrpc.CipherBoxDesc{
			StreamId: streamID,
		},
		Msg: payload,
	})
	if err != nil {
		return true, ClientStatusNotConnected, err
	}

	return false, ClientStatusConnected, nil
}

// CloseReceive will close the transport's connection to the receive-stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (gt *grpcTransport) CloseReceive() error {
	if gt.receiveStream == nil {
		return nil
	}

	return gt.receiveStream.CloseSend()
}

// CloseSend will close the transport's connection to the send-stream.
//
// NOTE: this is part of the ClientConnTransport interface.
func (gt *grpcTransport) CloseSend() error {
	if gt.sendStream == nil {
		return nil
	}

	return gt.sendStream.CloseSend()
}
