package gbn

import (
	"context"
	"io"
	"time"
)

// NewServerConn creates a new bidirectional Go-Back-N server.
// The sendStream function must write to the underlying transport stream.
// The receiveStream function must read from an underlying transport stream.
// The resendTimeout parameter defines the duration to wait before resending data
// if the corresponding ACK for the data is not received.
func NewServerConn(ctx context.Context, sendFunc sendBytesFunc,
	recvFunc recvBytesFunc, opts ...Option) (*GoBackNConn, error) {

	conn := newGoBackNConn(ctx, sendFunc, recvFunc, true, DefaultN)

	// Apply functional options
	for _, o := range opts {
		o(conn)
	}

	if err := conn.serverHandshake(); err != nil {
		if err := conn.Close(); err != nil {
			log.Errorf("error closing ServerConn: %v", err)
		}

		return nil, err
	}
	conn.start()

	return conn, nil
}

// serverHandshake initiates the server side GBN handshake.
// The server handshake sequence is as follows:
// 1.  The server waits for a SYN message from the client.
// 2.  The server then responds with a SYN message.
// 3.  The server waits for a SYNACK message from the client.
// 4a. If the server receives the SYNACK message before a resendTimeout, the hand
//     is considered complete.
// 4b. If SYNACK is not received before a certain resendTimeout
func (g *GoBackNConn) serverHandshake() error { // nolint:gocyclo
	recvChan := make(chan []byte)
	recvNext := make(chan int, 1)
	errChan := make(chan error, 1)
	handshakeComplete := make(chan struct{})
	defer close(handshakeComplete)

	go func() {
		for {
			select {
			case <-handshakeComplete:
				return
			case <-g.ctx.Done():
				return
			case <-g.quit:
				return
			case <-recvNext:
			}

			b, err := g.recvFromStream(g.ctx)
			if err != nil {
				errChan <- err
				return
			}

			select {
			case <-g.ctx.Done():
				return
			case <-g.quit:
				return
			case <-handshakeComplete:
				return
			case recvChan <- b:
			}
		}
	}()

	var n uint8

	for {
		log.Debugf("Waiting for client SYN")
		select {
		case <-g.ctx.Done():
			return nil
		case <-g.quit:
			return nil
		case recvNext <- 1:
		default:
		}

		var b []byte
		select {
		case <-g.ctx.Done():
			return nil
		case <-g.quit:
			return nil
		case b = <-recvChan:
		}

		msg, err := Deserialize(b)
		if err != nil {
			return err
		}

		switch msg.(type) {
		case *PacketSYN:
		default:
			log.Tracef("Expected SYN, got %T", msg)
			continue
		}

	recvClientSYN:

		log.Debugf("Received client SYN. Sending back.")
		n = msg.(*PacketSYN).N

		// Send SYN back
		syn := &PacketSYN{N: n}
		b, err = syn.Serialize()
		if err != nil {
			return err
		}

		if err = g.sendToStream(g.ctx, b); err != nil {
			return err
		}

		// Wait for SYNACK
		log.Debugf("Waiting for client SYNACK")
		select {
		case recvNext <- 1:
		case <-g.ctx.Done():
			return g.ctx.Err()
		case <-g.quit:
			return nil
		default:
		}

		select {
		case <-time.After(g.handshakeTimeout):
			log.Debugf("SYNCACK resendTimeout. Abort and wait " +
				"for client to re-initiate")
			continue
		case err := <-errChan:
			return err
		case <-g.ctx.Done():
			return nil
		case <-g.quit:
			return nil
		case b = <-recvChan:
		}

		msg, err = Deserialize(b)
		if err != nil {
			return err
		}

		switch msg.(type) {
		case *PacketSYNACK:
			break
		case *PacketSYN:
			log.Debugf("Received SYN. Resend SYN.")
			goto recvClientSYN
		default:
			return io.EOF
		}
		break
	}

	log.Debugf("Received SYNACK")

	// Set all variables that are dependent on the value of N that we get
	// from the client
	g.setN(n)

	log.Debugf("Handshake complete (Server)")
	return nil
}
