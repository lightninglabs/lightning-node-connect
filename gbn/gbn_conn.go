package gbn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	errTransportClosing = errors.New("gbn transport is closing")
)

type GoBackNConn struct {
	// n is the window size. The sender can send a maximum of n packets
	// before requiring an ack from the receiver for the first packet in
	// the window. The value of n is chosen by the client during the
	// GoBN handshake.
	n uint8

	// s is the maximum sequence number used to label packets. Packets
	// are labelled with incrementing sequence numbers modulo the window
	// size, n. s must be strictly larger than the window size, n. This
	// is so that the receiver can tell if the sender is resending the
	// previous window (maybe the sender did not receive the acks) or if
	// they are sending the next window. If s <= n then there would be
	// no way to tell.
	s uint8

	// sendSeqBase keeps track of the base of the send window and so
	// represents the next ack that we expect from the receiver. The
	// maximum value of sendSeqBase is s.
	// sendSeqBase must be guarded by senSeqMu.
	sendSeqBase uint8

	// sendSeqTop is the sequence number of the latest packet.
	// The difference between sendSeqTop and sendSeqBase should never
	// exceed the window size, n. The maximum value of sendSeqBase is s.
	// sendSeqTop must be guarded by senSeqMu.
	sendSeqTop uint8

	// sendSeqMu is used to guard sendSeqBase and sendSeqTop
	sendSeqMu sync.Mutex

	// sendQueueTop keeps track of the current position in the queue of
	// the packet with sequence number of sendSeqTop. This number is has a
	// maximum value of n.
	sendQueueTop uint8

	// recvSeq keeps track of the latest, correctly sequenced packet
	// sequence that we have received.
	recvSeq uint8

	// initRecv is true if we have received our first PacketData
	initRecv bool

	timeout time.Duration

	recvFromStream func(ctx context.Context) ([]byte, error)
	sendToStream   func(ctx context.Context, b []byte) error

	recvDataChan chan *PacketData
	sendDataChan chan []byte

	isServer bool

	errChan chan error

	// quit is used to stop the normal operations of the connection.
	// Once closed, the send and receive streams will still be available
	// for the FIN sequence.
	quit chan struct{}

	handshakeComplete chan struct{}

	ctx    context.Context
	cancel func()

	remoteClosed bool

	wg sync.WaitGroup
}

// Send blocks until an ack is received for the packet sent N packets before.
func (g *GoBackNConn) Send(data []byte) error {
	// Wait for handshake to complete
	select {
	case <-g.quit:
		return io.EOF
	case <-g.handshakeComplete:
	}

	select {
	case g.sendDataChan <- data:
		return nil
	case err := <-g.errChan:
		return fmt.Errorf("cannot send, gbn exited: %v", err)
	case <-g.quit:
	}
	return io.EOF
}

// Recv blocks until it gets a recv with the correct sequence it was expecting.
func (g *GoBackNConn) Recv() ([]byte, error) {
	// Wait for handshake to complete
	select {
	case <-g.quit:
		return nil, io.EOF
	case <-g.handshakeComplete:
	}

	select {
	case msg := <-g.recvDataChan:
		return msg.Payload, nil
	case err := <-g.errChan:
		return nil, fmt.Errorf("cannot receive, gbn exited: %v", err)
	case <-g.quit:
	}
	return nil, io.EOF
}

// start starts the various go routines needed by GoBackNConn.
// start should only be called once the handshake has been completed.
func (g *GoBackNConn) start() {
	log.Debugf("Starting (isServer=%v)", g.isServer)

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		err := g.receivePacketsForever()
		if err != nil {
			log.Debugf("Error in receivePacketsForever (isServer=%v): "+
				"%v", g.isServer, err)
			g.errChan <- err
		}
		log.Debugf("receivePacketsForever stopped (isServer=%v)", g.isServer)
	}()

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		err := g.sendPacketsForever()
		if err != nil {
			log.Debugf("Error in sendPacketsForever "+
				"(isServer=%v): %v", g.isServer, err)

			g.errChan <- err
		}
		log.Debugf("sendPacketsForever stopped (isServer=%v)",
			g.isServer)
	}()
}

// Close attempts to cleanly close the connection by sending a FIN message.
func (g *GoBackNConn) Close() error {

	log.Debugf("Closing GoBackNConn, isServer=%v", g.isServer)

	// We close the quit channel to stop the usual operations of the
	// server.
	close(g.quit)

	// If a connection had been established, try send a FIN message to
	// the peer if they have not already done so.
	select {
	case <-g.handshakeComplete:
		if !g.remoteClosed {
			log.Debugf("Try sending FIN, isServer=%v", g.isServer)
			ctxc, _ := context.WithTimeout(g.ctx, 1000*time.Millisecond)
			if err := g.sendPacket(ctxc, &PacketFIN{}); err != nil {
				log.Errorf("Error sending FIN: %v", err)
			}
		}
	default:
	}

	log.Debugf("canceling context")
	// Canceling the context will ensure that we are not hanging on the
	// receive or send functions passed to the server on initialisation.
	g.cancel()

	g.wg.Wait()
	log.Debugf("GBN is closed, isServer=%v", g.isServer)

	return nil
}

// receivePacketsForever  uses the provided recvFromStream to get new data from the
// underlying receive stream. It then checks to see if what was received is
// data or an ACK signal and then processes the packet accordingly.
//
// This function must be called in a go routine
func (g *GoBackNConn) receivePacketsForever() error {
	for {
		select {
		case <-g.quit:
			return nil
		default:
		}

		b, err := g.recvFromStream(g.ctx)
		if err != nil {
			return fmt.Errorf("error receiving "+
				"from recvFromStream: %s", err)
		}

		msg, err := Deserialize(b)
		if err != nil {
			return fmt.Errorf("deserialize error: %s", err)
		}

		switch m := msg.(type) {
		case *PacketData:
			// We receive a data packet with a sequence number
			// that we were not expecting.
			if m.Seq != g.recvSeq {
				// If this is our first recv, do nothing.
				// Let the sender timeout and resend.
				if !g.initRecv {
					continue
				}

				// else, send an ack for the last seq that
				// we received successfully.
				prevSeq := subAndMod(g.recvSeq, 1, g.s)
				ack := &PacketACK{
					prevSeq,
				}

				log.Debugf("got wrong data seq %d so "+
					"sending ack for %d (isServer=%v)",
					m.Seq, prevSeq, g.isServer)

				if err = g.sendPacket(g.ctx, ack); err != nil {
					return err
				}

				continue
			}

			log.Debugf("got correct data. sending ack %d "+
				"(isServer=%v)", g.recvSeq, g.isServer)

			// We received a packet with the expected seq. So send
			// an ack for it and increment the expected seq number.
			ack := &PacketACK{
				g.recvSeq,
			}

			if err = g.sendPacket(g.ctx, ack); err != nil {
				return err
			}

			g.initRecv = true
			g.recvSeq = (g.recvSeq + 1) % g.s

			select {
			case g.recvDataChan <- m:
			case <-g.quit:
				return nil
			}

		case *PacketACK:
			if m.Seq != g.sendSeqBase {
				// We received an unexpected sequence number.
				log.Debugf("got wrong ack %d expected "+
					"%d (isServer=%v)", m.Seq, g.sendSeqBase,
					g.isServer)

				// Determine if this is an ack for something in
				// the current window (between base and base + queue size)
				// If it is then update base and send ack for updated base.
				// Also decrement queue size by appropriate amount.
				log.Debugf("check if in queue? (isServer=%v)", g.isServer)
				if !g.isInQueue(m.Seq) {
					log.Debugf("not in queue (isServer=%v)", g.isServer)
					break
				}
				log.Debugf("is in queue (isServer=%v)", g.isServer)

				g.sendSeqMu.Lock()
				difference := subAndMod(m.Seq, g.sendSeqBase, g.s)
				log.Debugf("diff %d (isServer=%v)", difference, g.isServer)
				g.sendSeqBase = (g.sendSeqBase + difference) % g.s
				g.sendSeqMu.Unlock()
				break
			}

			log.Debugf("got correct ack %d (isServer=%v)", g.sendSeqBase, g.isServer)

			// We received the expected sequence number.
			// Decrement queue size and increment the base
			g.sendSeqBase = (g.sendSeqBase + 1) % g.s

		case *PacketFIN:
			log.Debugf("Got FIN packet (isServer=%v)", g.isServer)
			g.remoteClosed = true

			return errTransportClosing

		default:
			return errors.New("received unexpected message")
		}
	}
}

// isInQueue is used to determine if a number, c, is between two other numbers,
// a and b, where all of the numbers lie in a finite field (modulo space).
func (g *GoBackNConn) isInQueue(seq uint8) bool {
	g.sendSeqMu.Lock()
	defer g.sendSeqMu.Unlock()

	a := g.sendSeqBase % g.n
	b := g.sendSeqTop % g.n
	c := seq % g.n
	log.Debugf("base %d, top %d, seq %d", a, b, c)

	if a == b {
		return false
	}

	if a < b {
		if a <= c && c < b {
			return true
		}
		return false
	}

	// b < a

	if c < b || a <= c {
		return true
	}

	return false
}

func (g *GoBackNConn) queueSize() uint8 {
	g.sendSeqMu.Lock()
	defer g.sendSeqMu.Unlock()

	if g.sendSeqTop >= g.sendSeqBase {
		return g.sendSeqTop - g.sendSeqBase
	}

	return g.sendSeqTop + (g.s - g.sendSeqBase)
}

// subAndMod does modulo subtraction with the result remaining in the
// positive space.
func subAndMod(i, s, n uint8) uint8 {
	nn := int16(n)
	return uint8((((int16(i) - int16(s)) % nn) + nn) % nn)
}

// sendPacket serializes a message and writes it to the underlying send stream.
func (g *GoBackNConn) sendPacket(ctx context.Context, msg Message) error {
	b, err := msg.Serialize()
	if err != nil {
		return fmt.Errorf("serialize error: %s", err)
	}

	err = g.sendToStream(ctx, b)
	if err != nil {
		return fmt.Errorf("error calling sendToStream: %s", err)
	}

	return nil
}

// sendPacketsForever manages the resending logic. It keeps a cache of up to
// N packets and manages the resending of packets if acks are not received for
// them. It reads new data from sendDataChan only when there is space in the
// queue.
func (g *GoBackNConn) sendPacketsForever() error {
	n := g.n

	var data []byte
	queue := make([]*PacketData, n)

	resendQueue := func() error {
		queueSize := g.queueSize()
		log.Debugf("queue size %d,  (isServer=%v)", queueSize, g.isServer)
		if queueSize == 0 {
			return nil
		}

		for i := queueSize; i > 0; i-- {
			packet := queue[subAndMod(g.sendQueueTop, i, n)]
			log.Debugf("resending %d, (isServer=%v)", packet.Seq, g.isServer)
			if err := g.sendPacket(g.ctx, packet); err != nil {
				return err
			}
		}
		return nil
	}

	for {
		// If the queue is empty, then wait on sendDataChan for more
		// data to send. Otherwise, maybe resend queue after a timeout.
		if g.queueSize() == 0 {
			log.Debugf("empty queue isServer=%v", g.isServer)
			select {
			case <-g.quit:
				return nil
			case data = <-g.sendDataChan:
			}
		} else {
			select {
			case <-g.quit:
				return nil
			case <-time.After(g.timeout):
				if err := resendQueue(); err != nil {
					return err
				}
				continue
			case data = <-g.sendDataChan:
			}
		}

		g.sendSeqMu.Lock()

		packet := &PacketData{
			Seq:     g.sendSeqTop,
			Payload: data,
		}

		queue[g.sendQueueTop] = packet

		g.sendSeqTop = (g.sendSeqTop + 1) % g.s
		g.sendQueueTop = (g.sendQueueTop + 1) % g.n
		g.sendSeqMu.Unlock()

		log.Debugf("sending new data %d, (isServer=%v)", packet.Seq, g.isServer)

		if err := g.sendPacket(g.ctx, packet); err != nil {
			return err
		}
		log.Debugf("sent new data %d, (isServer=%v)", packet.Seq, g.isServer)

		if g.queueSize() < g.n {
			continue
		}

		for {
			log.Debugf("queue is full. wait for acks (isServer=%v)", g.isServer)

			// Need to wait for acks. Wait for timeout and then check again.
			// TODO(elle): should select on a signal that alerts us
			// of the queue size decreasing or base seq incrementing.
			select {
			case <-time.After(g.timeout):
			case <-g.quit:
				return nil
			}

			if g.queueSize() < g.n {
				break
			}

			// Since we have now waited for the timeout and still have not
			// received necessary acks, we resend the queue
			if err := resendQueue(); err != nil {
				return err
			}
		}
	}
}
