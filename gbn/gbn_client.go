package gbn

import (
	"context"
	"fmt"
	"io"
	"math"
	"time"
)

// NewClientConn creates a new bidirectional Go-Back-N client.
// The sendStream function must write to the underlying transport stream.
// The receiveStream function must read from an underlying transport stream.
// The timeout parameter defines the duration to wait before resending data
// if the corresponding ACK for the data is not received.
func NewClientConn(n uint8,
	sendToStream func(ctx context.Context, b []byte) error,
	receiveFromStream func(ctx context.Context) ([]byte, error),
	timeout time.Duration) (*GoBackNConn, error) {

	ctx, cancel := context.WithCancel(context.Background())

	if n == math.MaxUint8 {
		return nil, fmt.Errorf("n must be smaller than %d",
			math.MaxUint8)
	}

	conn := &GoBackNConn{
		n:                 n,
		s:                 n + 1,
		timeout:           timeout,
		recvFromStream:    receiveFromStream,
		sendToStream:      sendToStream,
		recvDataChan:      make(chan *PacketData, n),
		errChan:           make(chan error, 3),
		sendDataChan:      make(chan []byte),
		quit:              make(chan struct{}),
		handshakeComplete: make(chan struct{}),
		isServer:          false,
		ctx:               ctx,
		cancel:            cancel,
	}

	go func() {
		if err := conn.clientHandshake(); err != nil {
			conn.errChan <- err
			return
		}
		conn.start()
	}()

	return conn, nil
}

// clientHandshake initiates the client side GBN handshake.
func (g *GoBackNConn) clientHandshake() error {
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

	var resp Message
handshake:
	for {
		// start Handshake
		msg := &PacketSYN{N: g.n}
		msgBytes, err := msg.Serialize()
		if err != nil {
			return err
		}

		// Send SYN
		log.Debugf("Client sending SYN")
		if err := g.sendToStream(g.ctx, msgBytes); err != nil {
			return err
		}

		for {
			// Wait for SYN
			log.Debugf("Client waiting for SYN")
			var b []byte
			select {
			case recvNext <- 1:
			default:
			}

			select {
			case <-time.Tick(time.Second * 2):
				log.Debugf("SYN timeout. Resending SYN.")
				continue handshake
			case err := <-errChan:
				return err
			case b = <-recvChan:
			}

			resp, err = Deserialize(b)
			if err != nil {
				return err
			}

			log.Debugf("Client got %T", resp)
			switch resp.(type) {
			case *PacketSYN:
				break handshake
			default:
			}
		}
	}

	log.Debugf("Client got SYN")
	if resp.(*PacketSYN).N != g.n {
		return io.EOF
	}

	// Send SYNACK
	log.Debugf("Client sending SYNACK")
	synack, err := new(PacketSYNACK).Serialize()
	if err != nil {
		return err
	}

	if err := g.sendToStream(g.ctx, synack); err != nil {
		return err
	}

	log.Debugf("Client Handshake complete")
	close(g.handshakeComplete)

	return nil
}
