package gbn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"
)

var (
	errTransportClosing = errors.New("gbn transport is closing")
	errKeepaliveTimeout = errors.New("no pong received")
	errSendTimeout      = errors.New("send timeout")
	errRecvTimeout      = errors.New("receive timeout")
)

const (
	DefaultN                = 20
	defaultHandshakeTimeout = 100 * time.Millisecond
	defaultResendTimeout    = 100 * time.Millisecond
	finSendTimeout          = 1000 * time.Millisecond
	DefaultSendTimeout      = math.MaxInt64
	DefaultRecvTimeout      = math.MaxInt64
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

	sendQueue *queue

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

	sendTimeout   time.Duration
	sendTimeoutMu sync.RWMutex

	recvTimeout   time.Duration
	recvTimeoutMu sync.RWMutex

	isServer bool

	// handshakeTimeout is the time after which the server or client
	// will abort and restart the handshake if the expected response is
	// not received from the peer.
	handshakeTimeout time.Duration

	// receivedACKSignal channel is used to signal that the queue size has
	// been decreased.
	receivedACKSignal chan struct{}

	// resendSignal is used to signal that normal operation sending should
	// stop and the current queue contents should first be resent. Note
	// that this channel should only be listened on in one place.
	resendSignal chan struct{}

	pingTime   time.Duration
	pongTime   time.Duration
	pingTicker *IntervalAwareForceTicker
	pongTicker *IntervalAwareForceTicker
	pongWait   chan struct{}

	ctx    context.Context
	cancel func()

	// remoteClosed is closed if the remote party initiated the FIN sequence.
	remoteClosed chan struct{}

	// quit is used to stop the normal operations of the connection.
	// Once closed, the send and receive streams will still be available
	// for the FIN sequence.
	quit      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// newGoBackNConn creates a GoBackNConn instance with all the members which
// are common between client and server initialised.
func newGoBackNConn(ctx context.Context, sendFunc sendBytesFunc,
	recvFunc recvBytesFunc, isServer bool, n uint8) *GoBackNConn {

	ctxc, cancel := context.WithCancel(ctx)

	return &GoBackNConn{
		n:                 n,
		s:                 n + 1,
		resendTimeout:     defaultResendTimeout,
		recvFromStream:    recvFunc,
		sendToStream:      sendFunc,
		recvDataChan:      make(chan *PacketData, n),
		sendDataChan:      make(chan *PacketData),
		isServer:          isServer,
		sendQueue:         newQueue(n+1, defaultHandshakeTimeout),
		handshakeTimeout:  defaultHandshakeTimeout,
		recvTimeout:       DefaultRecvTimeout,
		sendTimeout:       DefaultSendTimeout,
		receivedACKSignal: make(chan struct{}),
		resendSignal:      make(chan struct{}, 1),
		remoteClosed:      make(chan struct{}),
		ctx:               ctxc,
		cancel:            cancel,
		quit:              make(chan struct{}),
	}
}

// setN sets the current N to use. This _must_ be set before the handshake is
// completed.
func (g *GoBackNConn) setN(n uint8) {
	g.n = n
	g.s = n + 1
	g.recvDataChan = make(chan *PacketData, n)
	g.sendQueue = newQueue(n+1, defaultHandshakeTimeout)
}

// SetSendTimeout sets the timeout used in the Send function.
func (g *GoBackNConn) SetSendTimeout(timeout time.Duration) {
	g.sendTimeoutMu.Lock()
	defer g.sendTimeoutMu.Unlock()

	g.sendTimeout = timeout
}

// SetRecvTimeout sets the timeout used in the Recv function.
func (g *GoBackNConn) SetRecvTimeout(timeout time.Duration) {
	g.recvTimeoutMu.Lock()
	defer g.recvTimeoutMu.Unlock()

	g.recvTimeout = timeout
}

// Send blocks until an ack is received for the packet sent N packets before.
func (g *GoBackNConn) Send(data []byte) error {
	// Wait for handshake to complete before we can send data.
	select {
	case <-g.quit:
		return io.EOF
	default:
	}

	g.sendTimeoutMu.RLock()
	ticker := time.NewTimer(g.sendTimeout)
	g.sendTimeoutMu.RUnlock()
	defer ticker.Stop()

	sendPacket := func(packet *PacketData) error {
		select {
		case g.sendDataChan <- packet:
			return nil
		case <-ticker.C:
			return errSendTimeout
		case <-g.quit:
			return fmt.Errorf("cannot send, gbn exited")
		}
	}

	if g.maxChunkSize == 0 {
		// Splitting is disabled.
		return sendPacket(&PacketData{
			Payload:    data,
			FinalChunk: true,
		})
	}

	// Splitting is enabled. Split into packets no larger than maxChunkSize.
	sentBytes := 0
	for sentBytes < len(data) {
		packet := &PacketData{}

		remainingBytes := len(data) - sentBytes
		if remainingBytes <= g.maxChunkSize {
			packet.Payload = data[sentBytes:]
			sentBytes += remainingBytes
			packet.FinalChunk = true
		} else {
			packet.Payload = data[sentBytes : sentBytes+g.maxChunkSize]
			sentBytes += g.maxChunkSize
		}

		if err := sendPacket(packet); err != nil {
			return err
		}
	}

	return nil
}

// Recv blocks until it gets a recv with the correct sequence it was expecting.
func (g *GoBackNConn) Recv() ([]byte, error) {
	// Wait for handshake to complete
	select {
	case <-g.quit:
		return nil, io.EOF
	default:
	}

	var (
		b   []byte
		msg *PacketData
	)

	g.recvTimeoutMu.RLock()
	ticker := time.NewTimer(g.recvTimeout)
	g.recvTimeoutMu.RUnlock()
	defer ticker.Stop()

	for {
		select {
		case <-g.quit:
			return nil, fmt.Errorf("cannot receive, gbn exited")
		case <-ticker.C:
			return nil, errRecvTimeout
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

	pingTime := time.Duration(math.MaxInt64)
	if g.pingTime != 0 {
		pingTime = g.pingTime
	}

	g.pingTicker = NewIntervalAwareForceTicker(pingTime)
	g.pingTicker.Resume()

	pongTime := time.Duration(math.MaxInt64)
	if g.pongTime != 0 {
		pongTime = g.pongTime
	}

	g.pongTicker = NewIntervalAwareForceTicker(pongTime)

	g.resendTicker = time.NewTicker(g.resendTimeout)

	g.wg.Add(1)
	go func() {
		defer func() {
			g.wg.Done()
			if err := g.Close(); err != nil {
				log.Errorf("error closing GoBackNConn: %v", err)
			}
		}()

		err := g.receivePacketsForever()
		if err != nil {
			log.Debugf("Error in receivePacketsForever "+
				"(isServer=%v): %v", g.isServer, err)
		}
		log.Debugf("receivePacketsForever stopped (isServer=%v)",
			g.isServer)
	}()

	g.wg.Add(1)
	go func() {
		defer func() {
			g.wg.Done()
			if err := g.Close(); err != nil {
				log.Errorf("error closing GoBackNConn: %v", err)
			}
		}()

		err := g.sendPacketsForever()
		if err != nil {
			log.Debugf("Error in sendPacketsForever "+
				"(isServer=%v): %v", g.isServer, err)

		}
		log.Debugf("sendPacketsForever stopped (isServer=%v)",
			g.isServer)
	}()
}

// Close attempts to cleanly close the connection by sending a FIN message.
func (g *GoBackNConn) Close() error {
	g.closeOnce.Do(func() {
		log.Debugf("Closing GoBackNConn, isServer=%v", g.isServer)

		// We close the quit channel to stop the usual operations of the
		// server.
		close(g.quit)

		// Try send a FIN message to the peer if they have not already
		// done so.
		select {
		case <-g.remoteClosed:
		default:
			log.Tracef("Try sending FIN, isServer=%v", g.isServer)
			ctxc, cancel := context.WithTimeout(
				g.ctx, finSendTimeout,
			)
			defer cancel()
			if err := g.sendPacket(ctxc, &PacketFIN{}); err != nil {
				log.Errorf("Error sending FIN: %v", err)
			}
		}

		// Canceling the context will ensure that we are not hanging on
		// the receive or send functions passed to the server on
		// initialisation.
		g.cancel()

		g.wg.Wait()

		g.pingTicker.Stop()
		g.resendTicker.Stop()

		log.Debugf("GBN is closed, isServer=%v", g.isServer)
	})

	return nil
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
	// resendQueue re-sends the current contents of the queue.
	resendQueue := func() error {
		return g.sendQueue.resend(func(packet *PacketData) error {
			return g.sendPacket(g.ctx, packet)
		})
	}

	for {
		// The queue is not empty. If we receive a resend signal
		// or if the resend timeout passes then we resend the
		// current contents of the queue. Otherwise, wait for
		// more data to arrive on sendDataChan.
		var packet *PacketData
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

		case <-g.pingTicker.Ticks():

			// Start the pong timer.
			g.pongTicker.Reset()
			g.pongTicker.Resume()

			log.Tracef("Sending a PING packet (isServer=%v)",
				g.isServer)

			packet = &PacketData{
				IsPing: true,
			}

		case <-g.pongTicker.Ticks():
			return errKeepaliveTimeout

		case packet = <-g.sendDataChan:
		}

		// New data has arrived that we need to add to the queue and
		// send.
		g.sendQueue.addPacket(packet)

		log.Tracef("Sending data %d", packet.Seq)
		if err := g.sendPacket(g.ctx, packet); err != nil {
			return err
		}

		for {
			// If the queue size is still less than N, we can
			// continue to add more packets to the queue.
			if g.sendQueue.size() < g.n {
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
			case <-g.resendSignal:
				if err := resendQueue(); err != nil {
					return err
				}
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
func (g *GoBackNConn) receivePacketsForever() error { // nolint:gocyclo
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

		// Reset the ping & pong timer if any packet is received.
		// If ping/pong is disabled, this is a no-op.
		g.pingTicker.Reset()
		if g.pongTicker.IsActive() {
			g.pongTicker.Pause()
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
			gotValidACK := g.sendQueue.processACK(m.Seq)
			if gotValidACK {
				g.resendTicker.Reset(g.resendTimeout)

				// Send a signal to indicate that new
				// ACKs have been received.
				select {
				case g.receivedACKSignal <- struct{}{}:
				default:
				}
			}

		case *PacketNACK:
			// We received a NACK packet. This means that the
			// receiver got a data packet that they were not
			// expecting. This likely means that a packet that we
			// sent was dropped, or maybe we sent a duplicate
			// message. The NACK message contains the sequence
			// number that the receiver was expecting.
			inQueue, bumped := g.sendQueue.processNACK(m.Seq)

			// If the NACK sequence number is not in our queue
			// then we ignore it. We must have received the ACK
			// for the sequence number in the meantime.
			if !inQueue {
				log.Tracef("NACK seq %d is not in the queue. "+
					"Ignoring. (isServer=%v)", m.Seq,
					g.isServer)
				continue
			}

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

			close(g.remoteClosed)

			return errTransportClosing

		default:
			return fmt.Errorf("received unexpected message: %T", msg)
		}
	}
}
