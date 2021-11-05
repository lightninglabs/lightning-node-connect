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
	errKeepaliveTimeout = errors.New("no pong received")
)

const (
	defaultHandshakeTimeout = 100 * time.Millisecond
	defaultResendTimeout    = 100 * time.Millisecond
	finSendTimeout          = 1000 * time.Millisecond
)

type sendBytesFunc func(ctx context.Context, b []byte) error
type recvBytesFunc func(ctx context.Context) ([]byte, error)

type GoBackNConn struct {
	// n is the window size. The sender can send a maximum of n packets
	// before requiring an ack from the receiver for the first packet in
	// the window. The value of n is chosen by the client during the
	// GoBN handshake.
	n uint8

	// s is the maximum sequence number used to label packets. Packets
	// are labelled with incrementing sequence numbers modulo s.
	// s must be strictly larger than the window size, n. This
	// is so that the receiver can tell if the sender is resending the
	// previous window (maybe the sender did not receive the acks) or if
	// they are sending the next window. If s <= n then there would be
	// no way to tell.
	s uint8

	// maxChunkSize is the maximum payload size in bytes allowed per
	// message. If the payload to be sent is larger than maxChunkSize then
	// the payload will be split between multiple packets.
	// If maxChunkSize is zero then it is disabled and data won't be split
	// between packets.
	maxChunkSize int

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

	// sendSeqTopMu is used to guard sendSeqTop.
	sendSeqTopMu sync.RWMutex

	// sendSeqBaseMu is used to guard sendSeqBase.
	sendSeqBaseMu sync.RWMutex

	// recvSeq keeps track of the latest, correctly sequenced packet
	// sequence that we have received.
	recvSeq uint8

	// resendTimeout is the duration that will be waited before resending
	// the packets in the current queue.
	resendTimeout time.Duration
	resendTicker  *time.Ticker

	recvFromStream recvBytesFunc
	sendToStream   sendBytesFunc

	recvDataChan chan *PacketData
	sendDataChan chan *PacketData

	isServer bool

	// handshakeTimeout is the time after which the server or client
	// will abort and restart the handshake if the expected response is
	// not received from the peer.
	handshakeTimeout time.Duration

	// handshakeComplete is used to signal if the handshake is complete
	// or not. If the channel is closed then the handshake is complete.
	handshakeComplete chan struct{}

	// receivedACKSignal channel is used to signal that the queue size has
	// been decreased. Note that this channel should only be listened on
	// in one place.
	receivedACKSignal chan struct{}

	// resendSignal is used to signal that normal operation sending should
	// stop and the current queue contents should first be resent. Note
	// that this channel should only be listened on in one place.
	resendSignal chan struct{}

	// lastResend is the time that the queue was last resent. This is
	// used to limit the frequency at which we resend the queue.
	lastResend time.Time

	pingTime   time.Duration
	pingTicker *time.Ticker
	pongWait   chan struct{}

	ctx    context.Context
	cancel func()

	// remoteClosed is true if the remote party initiated the FIN sequence.
	remoteClosed bool

	// quit is used to stop the normal operations of the connection.
	// Once closed, the send and receive streams will still be available
	// for the FIN sequence.
	quit chan struct{}

	wg sync.WaitGroup

	errChan chan error
}

// newGoBackNConn creates a GoBackNConn instance with all the members which
// are common between client and server initialised.
func newGoBackNConn(ctx context.Context, sendFunc sendBytesFunc,
	recvFunc recvBytesFunc, isServer bool) *GoBackNConn {

	ctxc, cancel := context.WithCancel(ctx)

	return &GoBackNConn{
		resendTimeout:     defaultResendTimeout,
		recvFromStream:    recvFunc,
		sendToStream:      sendFunc,
		sendDataChan:      make(chan *PacketData),
		isServer:          isServer,
		handshakeTimeout:  defaultHandshakeTimeout,
		handshakeComplete: make(chan struct{}),
		receivedACKSignal: make(chan struct{}),
		resendSignal:      make(chan struct{}, 1),
		ctx:               ctxc,
		cancel:            cancel,
		quit:              make(chan struct{}),
		errChan:           make(chan error, 3),
	}
}

// Send blocks until an ack is received for the packet sent N packets before.
func (g *GoBackNConn) Send(data []byte) error {
	// Wait for handshake to complete
	select {
	case <-g.quit:
		return io.EOF
	case <-g.handshakeComplete:
	}

	if g.maxChunkSize == 0 {
		// Splitting is disabled

		packet := &PacketData{
			Payload:    data,
			FinalChunk: true,
		}
		select {
		case g.sendDataChan <- packet:
			return nil
		case err := <-g.errChan:
			return fmt.Errorf("cannot send, gbn exited: %v", err)
		case <-g.quit:
			return io.EOF
		}
	}

	// Splitting is enabled. Split into packets no larger than g.maxChunkSize
	//
	// TODO(elle): use offsets rather than copying a possibly large slice
	// of bytes
	d := make([]byte, len(data))
	copy(d, data)
	for len(d) > 0 {
		packet := &PacketData{}

		if len(d) < g.maxChunkSize {
			packet.Payload = d
		} else {
			packet.Payload = d[:g.maxChunkSize]
		}

		d = d[len(packet.Payload):]
		if len(d) == 0 {
			packet.FinalChunk = true
		}

		select {
		case g.sendDataChan <- packet:
			continue
		case err := <-g.errChan:
			return fmt.Errorf("cannot send, gbn exited: %v", err)
		case <-g.quit:
		}
		return io.EOF
	}

	return nil
}

// Recv blocks until it gets a recv with the correct sequence it was expecting.
func (g *GoBackNConn) Recv() ([]byte, error) {
	// Wait for handshake to complete
	select {
	case <-g.quit:
		return nil, io.EOF
	case <-g.handshakeComplete:
	}

	var (
		b   []byte
		msg *PacketData
	)

	for {
		select {
		case err := <-g.errChan:
			return nil, fmt.Errorf("cannot receive, gbn exited: %v",
				err)
		case <-g.quit:
			return nil, io.EOF
		case msg = <-g.recvDataChan:
		}

		b = append(b, msg.Payload...)

		if msg.FinalChunk {
			break
		}
	}

	return b, nil
}

// start kicks off the various goroutines needed by GoBackNConn.
// start should only be called once the handshake has been completed.
func (g *GoBackNConn) start() {
	log.Debugf("Starting (isServer=%v)", g.isServer)

	g.pingTicker = &time.Ticker{}
	if g.pingTime != 0 {
		g.pingTicker = time.NewTicker(g.pingTime)
	}

	g.resendTicker = time.NewTicker(g.resendTimeout)

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		err := g.receivePacketsForever()
		if err != nil {
			log.Debugf("Error in receivePacketsForever "+
				"(isServer=%v): %v", g.isServer, err)
			g.errChan <- err
		}
		log.Debugf("receivePacketsForever stopped (isServer=%v)",
			g.isServer)
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
			log.Tracef("Try sending FIN, isServer=%v", g.isServer)
			ctxc, cancel := context.WithTimeout(
				g.ctx, finSendTimeout,
			)
			defer cancel()
			if err := g.sendPacket(ctxc, &PacketFIN{}); err != nil {
				log.Errorf("Error sending FIN: %v", err)
			}
		}
	default:
	}

	// Canceling the context will ensure that we are not hanging on the
	// receive or send functions passed to the server on initialisation.
	g.cancel()

	g.wg.Wait()
	log.Debugf("GBN is closed, isServer=%v", g.isServer)

	return nil
}

// isInQueue is used to determine if a number, c, is between two other numbers,
// a and b, where all of the numbers lie in a finite field (modulo space) s.
func isInQueue(a, b, c uint8) bool {
	// if a and b are equal then the queue is empty.
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

// queueSize is used to calculate the current sender queueSize.
func (g *GoBackNConn) queueSize() uint8 {
	g.sendSeqBaseMu.RLock()
	g.sendSeqTopMu.RLock()

	defer func() {
		g.sendSeqBaseMu.RUnlock()
		g.sendSeqTopMu.RUnlock()
	}()

	if g.sendSeqTop >= g.sendSeqBase {
		return g.sendSeqTop - g.sendSeqBase
	}

	return g.sendSeqTop + (g.s - g.sendSeqBase)
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
// them or if NACKs are received. It reads new data from sendDataChan only
// when there is space in the queue.
//
// This function must be called in a go routine.
func (g *GoBackNConn) sendPacketsForever() error {
	var packet *PacketData
	queue := make([]*PacketData, g.s)

	// resendQueue re-sends the current contents of the queue.
	resendQueue := func() error {
		if time.Since(g.lastResend) < g.handshakeTimeout {
			log.Tracef("Resent the queue recently.")

			return nil
		}

		if g.queueSize() == 0 {
			return nil
		}

		g.lastResend = time.Now()

		g.sendSeqTopMu.RLock()
		top := g.sendSeqTop
		g.sendSeqTopMu.RUnlock()

		g.sendSeqBaseMu.RLock()
		base := g.sendSeqBase
		g.sendSeqBaseMu.RUnlock()

		if base == top {
			return nil
		}

		log.Tracef("Resending the queue")

		for base != top {
			packet := queue[base]

			if err := g.sendPacket(g.ctx, packet); err != nil {
				return err
			}
			base = (base + 1) % g.s

			log.Tracef("Resent %d", packet.Seq)
		}

		return nil
	}

	for {
		// The queue is not empty. If we receive a resend signal
		// or if the resend timeout passes then we resend the
		// current contents of the queue. Otherwise, wait for
		// more data to arrive on sendDataChan.
		select {
		case <-g.quit:
			return nil

		case <-g.resendSignal:
			if err := resendQueue(); err != nil {
				return err
			}
			continue

		case <-g.resendTicker.C:
			if err := resendQueue(); err != nil {
				return err
			}
			continue

		case <-g.pingTicker.C:
			select {
			case g.pongWait <- struct{}{}:
			default:
				// already waiting for pong. Timed
				// out. close conn.
				return errKeepaliveTimeout
			}

			log.Tracef("Sending a PING packet (isServer=%v)",
				g.isServer)

			packet = &PacketData{
				IsPing: true,
			}

		case packet = <-g.sendDataChan:
		}

		// New data has arrived that we need to add to the queue and
		// send.

		g.sendSeqTopMu.Lock()

		// Give the new data a sequence number and add it to the queue
		// and increment sendSeqTop to reflect the queue size increase.
		packet.Seq = g.sendSeqTop
		queue[g.sendSeqTop] = packet
		g.sendSeqTop = (g.sendSeqTop + 1) % g.s

		g.sendSeqTopMu.Unlock()

		log.Tracef("Sending data %d", packet.Seq)

		if err := g.sendPacket(g.ctx, packet); err != nil {
			return err
		}

		for {
			// If the queue size is still less than N, we can
			// continue to add more packets to the queue.
			if g.queueSize() < g.n {
				break
			}

			log.Tracef("The queue is full.")

			// The queue is full. We wait for a ACKs to arrive or
			// resend the queue after a timeout.
			select {
			case <-g.quit:
				return nil
			case <-g.receivedACKSignal:
				break
			case <-g.resendTicker.C:
				if err := resendQueue(); err != nil {
					return err
				}
			}
		}
	}
}

// receivePacketsForever uses the provided recvFromStream to get new data
// from the underlying transport. It then checks to see if what was received is
// data, an ACK, NACK or FIN signal and then processes the packet accordingly.
//
// This function must be called in a go routine.
func (g *GoBackNConn) receivePacketsForever() error {
	var (
		lastNackSeq  uint8
		lastNackTime time.Time
	)

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

		// If keepalive is enabled, reset the ping timer if any packet
		// is received and remove any contents from the pongWait
		// channel.
		if g.pingTime != 0 {
			g.pingTicker.Reset(g.pingTime)
			select {
			case <-g.pongWait:
			default:
			}
		}

		switch m := msg.(type) {
		case *PacketData:
			switch m.Seq == g.recvSeq {
			case true:
				// We received a data packet with the sequence
				// number we were expecting. So we respond with
				// an ACK message with that sequence number
				// and we bump the sequence number that we
				// expect of the next data packet.
				log.Tracef("Got expected data %d", m.Seq)

				ack := &PacketACK{
					Seq: m.Seq,
				}

				if err = g.sendPacket(g.ctx, ack); err != nil {
					return err
				}

				g.recvSeq = (g.recvSeq + 1) % g.s

				// If the packet was a ping, then there is no
				// data to return to the above layer.
				if m.IsPing {
					continue
				}

				// Pass the returned packet to the layer above
				// GBN.
				select {
				case g.recvDataChan <- m:
				case <-g.quit:
					return nil
				}

			case false:
				// We received a data packet with a sequence
				// number that we were not expecting. This
				// could be a packet that we have already
				// received and that is being resent because
				// the ACK for it was not received in time or
				// it could be that we missed a previous packet.
				// In either case, we send a NACK with the
				// sequence number that we were expecting.
				log.Tracef("Got unexpected data %d", m.Seq)

				// If we recently sent a NACK for the same
				// sequence number then back off.
				if lastNackSeq == g.recvSeq &&
					time.Since(lastNackTime) < g.resendTimeout {

					continue
				}

				log.Tracef("Sending NACK %d", g.recvSeq)

				// Send a NACK with the expected sequence
				// number.
				nack := &PacketNACK{
					Seq: g.recvSeq,
				}

				if err = g.sendPacket(g.ctx, nack); err != nil {
					return err
				}

				lastNackTime = time.Now()
				lastNackSeq = nack.Seq
			}

		case *PacketACK:
			g.sendSeqBaseMu.Lock()

			switch m.Seq == g.sendSeqBase {
			case true:
				// We received an ACK packet with the sequence
				// number that is equal to the one we were
				// expecting. So we increase our base
				// accordingly and send a signal to indicate
				// that the queue size has decreased.
				log.Tracef("Received correct ack %d", m.Seq)

				g.sendSeqBase = (g.sendSeqBase + 1) % g.s
				g.resendTicker.Reset(g.resendTimeout)

				select {
				case g.receivedACKSignal <- struct{}{}:
				default:
				}

			case false:
				// We received an ACK with a sequence number
				// that we were not expecting. This could
				// be a duplicate ACK before or it could be
				// that we just missed the ACK for the current
				// base and this is actually an ACK for
				// another packet in the queue.
				log.Tracef("Received wrong ack %d, expected %d",
					m.Seq, g.sendSeqBase)

				// If this is an ACK for something
				// in the current queue then maybe we just
				// missed a previous ACK. We can bump the
				// base to be equal to this sequence number.
				g.sendSeqTopMu.RLock()

				if isInQueue(g.sendSeqBase, g.sendSeqTop,
					m.Seq) {

					log.Tracef("Sequence %d is in the "+
						"queue. Bump the base.", m.Seq)

					g.sendSeqBase = (m.Seq + 1) % g.s
					g.resendTicker.Reset(g.resendTimeout)

					// Send a signal to indicate that new
					// ACKs have been received.
					select {
					case g.receivedACKSignal <- struct{}{}:
					default:
					}
				}
				g.sendSeqTopMu.RUnlock()
			}
			g.sendSeqBaseMu.Unlock()

		case *PacketNACK:
			// We received a NACK packet. This means that the
			// receiver got a data packet that they were not
			// expecting. This likely means that a packet that we
			// sent was dropped or maybe we sent a duplicte message.
			// The NACK message contains the sequence number that
			// the receiver was expecting.
			log.Tracef("Received NACK %d", m.Seq)

			g.sendSeqBaseMu.Lock()
			g.sendSeqTopMu.RLock()

			// If the NACK sequence number is not in our queue
			// then we ignore it. We must have received the ACK
			// for the sequence number in the mean time.
			if !isInQueue(g.sendSeqBase, g.sendSeqTop, m.Seq) {
				g.sendSeqTopMu.RUnlock()
				g.sendSeqBaseMu.Unlock()

				log.Tracef("NACK seq %d is not in the queue. "+
					"Ignoring. (isServer=%v)", m.Seq,
					g.isServer)
				continue
			}

			g.sendSeqTopMu.RUnlock()

			// The NACK sequence is in the queue. So we bump the
			// base to be whatever the sequence is.
			bumped := false
			if g.sendSeqBase != m.Seq {
				bumped = true
			}

			g.sendSeqBase = m.Seq
			g.sendSeqBaseMu.Unlock()

			// If the base was bumped, then the queue is now smaller
			// and so we can send a signal to indicate this.
			if bumped {
				select {
				case g.receivedACKSignal <- struct{}{}:
				default:
				}
			}

			log.Tracef("Sending a resend signal (isServer=%v)",
				g.isServer)

			// Send a signal to indicate that new sends should pause
			// and the current queue should be resent instead.
			select {
			case g.resendSignal <- struct{}{}:
			default:
			}

		case *PacketFIN:
			// A FIN packet indicates that the peer would like to
			// close the connection.

			log.Tracef("Received a FIN packet (isServer=%v)",
				g.isServer)

			g.remoteClosed = true

			return errTransportClosing

		default:
			return errors.New("received unexpected message")
		}
	}
}
