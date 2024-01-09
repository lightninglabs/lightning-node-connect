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

	cfg := newConfig(sendFunc, recvFunc, DefaultN)

	// Apply functional options
	for _, o := range opts {
		o(cfg)
	}

	conn := newGoBackNConn(ctx, cfg, "server")

	if err := conn.serverHandshake(); err != nil {
		if err := conn.Close(); err != nil {
			conn.log.Errorf("Error closing ServerConn: %v", err)
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
// 4a. If the server receives the SYNACK message before a resendTimeout, the
// handshake is considered complete.
// 4b. If SYNACK is not received before a certain resendTimeout, then the
// handshake is aborted and the process is started from step 1 again.
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

			b, err := g.cfg.recvFromStream(g.ctx)
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
	var resent bool

handshakeLoop:
	for {
		g.log.Debugf("Waiting for client SYN")
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

		case *PacketSYNACK, *PacketData:
			// If we receive a SYNACK or DATA packet after we have
			// restarted the handshake, we can be sure that the
			// client has received our SYN and has completed the
			// handshake. We can therefore complete the handshake
			// ourselves.
			if resent {
				g.log.Tracef("Received %T after restarting "+
					"handshake", msg)
				g.timeoutManager.Received(msg)

				break handshakeLoop
			}

			g.log.Tracef("Expected SYN, got %T", msg)

			continue
		default:
			g.log.Tracef("Expected SYN, got %T", msg)
			continue
		}

	recvClientSYN:

		g.log.Debugf("Received client SYN. Sending back.")
		n = msg.(*PacketSYN).N

		// Send SYN back
		syn := &PacketSYN{N: n}
		b, err = syn.Serialize()
		if err != nil {
			return err
		}

		if err = g.cfg.sendToStream(g.ctx, b); err != nil {
			return err
		}

		// Notify the timeout manager that we sent a SYN.
		g.timeoutManager.Sent(msg, resent)

		// Wait for SYNACK
		g.log.Debugf("Waiting for client SYNACK")
		select {
		case recvNext <- 1:
		case <-g.ctx.Done():
			return g.ctx.Err()
		case <-g.quit:
			return nil
		default:
		}

		select {
		case <-time.After(g.timeoutManager.GetHandshakeTimeout()):
			g.log.Debugf("SYNCACK resendTimeout. Abort and wait " +
				"for client to re-initiate")
			resent = true

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
			g.log.Debugf("Received SYNACK")

			// Notify the timeout manager we've received the SYNACK
			// response from the counterparty.
			g.timeoutManager.Received(msg)

			break
		case *PacketSYN:
			g.log.Debugf("Received SYN. Resend SYN.")
			resent = true

			goto recvClientSYN
		default:
			return io.EOF
		}
		break
	}

	// Set all variables that are dependent on the value of N that we get
	// from the client
	g.setN(n)

	g.log.Debugf("Handshake complete (Server)")

	return nil
}
