package gbn

import (
	"context"
	"io"
	"time"
)

var count int

// NewServerConn creates a new bidirectional Go-Back-N server.
// The sendStream function must write to the underlying transport stream.
// The receiveStream function must read from an underlying transport stream.
// The timeout parameter defines the duration to wait before resending data
// if the corresponding ACK for the data is not received.
func NewServerConn(ctx context.Context,
	sendToStream func(ctx context.Context, b []byte) error,
	recvFromStream func(ctx context.Context) ([]byte, error),
	timeout time.Duration) (*GoBackNConn, error) {

	ctxc, cancel := context.WithCancel(ctx)

	conn := &GoBackNConn{
		timeout:           timeout,
		recvFromStream:    recvFromStream,
		sendToStream:      sendToStream,
		errChan:           make(chan error, 3),
		sendDataChan:      make(chan []byte),
		quit:              make(chan struct{}),
		handshakeComplete: make(chan struct{}),
		isServer:          true,
		ctx:               ctxc,
		cancel:            cancel,
	}

	go func() {
		if err := conn.serverHandshake(); err != nil {
			conn.errChan <- err
			return
		}
		conn.start()
	}()

	return conn, nil
}

// serverHandshake initiates the server side GBN handshake.
func (g *GoBackNConn) serverHandshake() error {
	recvChan := make(chan []byte)
	recvNext := make(chan int)
	errChan := make(chan error, 1)
	go func() {
		for {
			select {
			case <-g.handshakeComplete:
				return
			case <-recvNext:
			}

			b, err := g.recvFromStream(g.ctx)
			if err != nil {
				errChan <- err
				return
			}
			recvChan <- b
		}
	}()

	log.Debugf("Waiting for client SYN")
	recvNext <- 1
	b := <-recvChan

	msg, err := Deserialize(b)
	if err != nil {
		return err
	}

	switch msg.(type) {
	case *PacketSYN:
	default:
		return io.EOF
	}

	log.Debugf("Received client SYN.")

	var n uint8

	for {
		n = msg.(*PacketSYN).N

		// Send SYN back
		log.Debugf("Sending SYN.")
		syn := &PacketSYN{N: n}
		b, err := syn.Serialize()
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
		default:
		}

		select {
		case <-time.Tick(2 * time.Second):
			log.Debugf("SYNCACK timeout. Resending SYN.")
			continue
		case err := <-errChan:
			return err
		case b = <-recvChan:
		}

		msg, err = Deserialize(b)
		if err != nil {
			return err
		}

		switch msg.(type) {
		case *PacketSYNACK:
			log.Debugf("Received SYNACK")
			break
		case *PacketSYN:
			log.Debugf("Received SYN. Resend SYN.")
			continue
		default:
			return io.EOF
		}
		break
	}

	g.n = n
	g.s = n + 1
	g.recvDataChan = make(chan *PacketData, n)

	close(g.handshakeComplete)

	log.Debugf("Handshake complete (Server)")
	return nil
}
