package gbn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/btcsuite/btclog"
	"github.com/lightningnetwork/lnd/build"
)

var (
	errTransportClosing = errors.New("gbn transport is closing")
	errKeepaliveTimeout = errors.New("no pong received")
	errSendTimeout      = errors.New("send timeout")
	errRecvTimeout      = errors.New("receive timeout")
)

const (
	DefaultN = 20
)

type sendBytesFunc func(ctx context.Context, b []byte) error
type recvBytesFunc func(ctx context.Context) ([]byte, error)

type GoBackNConn struct {
	cfg *config

	sendQueue *queue

	// recvSeq keeps track of the latest, correctly sequenced packet
	// sequence that we have received.
	recvSeq uint8

	resendTicker *time.Ticker

	recvDataChan chan *PacketData
	sendDataChan chan *PacketData

	log btclog.Logger

	// receivedACKSignal channel is used to signal that the queue size has
	// been decreased.
	receivedACKSignal chan struct{}

	// resendSignal is used to signal that normal operation sending should
	// stop and the current queue contents should first be resent. Note
	// that this channel should only be listened on in one place.
	resendSignal chan struct{}

	pingTicker *IntervalAwareForceTicker
	pongTicker *IntervalAwareForceTicker

	ctx    context.Context //nolint:containedctx
	cancel func()

	// remoteClosed is closed if the remote party initiated the FIN sequence.
	remoteClosed chan struct{}

	// timeoutManager is used to manage all the timeouts used by the
	// GoBackNConn.
	timeoutManager *TimeoutManager

	// quit is used to stop the normal operations of the connection.
	// Once closed, the send and receive streams will still be available
	// for the FIN sequence.
	quit      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// newGoBackNConn creates a GoBackNConn instance with all the members which
// are common between client and server initialised.
func newGoBackNConn(ctx context.Context, cfg *config,
	loggerPrefix string) *GoBackNConn {

	ctxc, cancel := context.WithCancel(ctx)

	// Construct a new prefixed logger.
	prefix := fmt.Sprintf("(%s)", loggerPrefix)
	plog := build.NewPrefixLog(prefix, log)

	timeoutManager := NewTimeOutManager(plog, cfg.timeoutOptions...)

	g := &GoBackNConn{
		cfg:               cfg,
		recvDataChan:      make(chan *PacketData, cfg.n),
		sendDataChan:      make(chan *PacketData),
		receivedACKSignal: make(chan struct{}),
		resendSignal:      make(chan struct{}, 1),
		remoteClosed:      make(chan struct{}),
		ctx:               ctxc,
		cancel:            cancel,
		log:               plog,
		quit:              make(chan struct{}),
		timeoutManager:    timeoutManager,
	}

	g.sendQueue = newQueue(
		&queueCfg{
			s:   cfg.n + 1,
			log: plog,
			sendPkt: func(packet *PacketData) error {
				return g.sendPacket(g.ctx, packet, true)
			},
		},
		timeoutManager,
	)

	return g
}

// SetSendTimeout sets the timeout used in the Send function.
func (g *GoBackNConn) SetSendTimeout(timeout time.Duration) {
	g.timeoutManager.SetSendTimeout(timeout)
}

// SetRecvTimeout sets the timeout used in the Recv function.
func (g *GoBackNConn) SetRecvTimeout(timeout time.Duration) {
	g.timeoutManager.SetRecvTimeout(timeout)
}

// setN sets the current N to use. This _must_ be set before the handshake is
// completed.
func (g *GoBackNConn) setN(n uint8) {
	g.cfg.n = n
	g.cfg.s = n + 1
	g.recvDataChan = make(chan *PacketData, n)
	g.sendQueue = newQueue(
		&queueCfg{
			s:   n + 1,
			log: g.log,
			sendPkt: func(packet *PacketData) error {
				return g.sendPacket(g.ctx, packet, true)
			},
		},
		g.timeoutManager,
	)
}

// Send blocks until an ack is received for the packet sent N packets before.
func (g *GoBackNConn) Send(data []byte) error {
	// Wait for handshake to complete before we can send data.
	select {
	case <-g.quit:
		return io.EOF
	default:
	}

	ticker := time.NewTimer(g.timeoutManager.GetSendTimeout())
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

	if g.cfg.maxChunkSize == 0 {
		// Splitting is disabled.
		return sendPacket(&PacketData{
			Payload:    data,
			FinalChunk: true,
		})
	}

	// Splitting is enabled. Split into packets no larger than maxChunkSize.
	var (
		sentBytes = 0
		maxChunk  = g.cfg.maxChunkSize
	)
	for sentBytes < len(data) {
		packet := &PacketData{}

		remainingBytes := len(data) - sentBytes
		if remainingBytes <= maxChunk {
			packet.Payload = data[sentBytes:]
			sentBytes += remainingBytes
			packet.FinalChunk = true
		} else {
			packet.Payload = data[sentBytes : sentBytes+maxChunk]
			sentBytes += maxChunk
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

	ticker := time.NewTimer(g.timeoutManager.GetRecvTimeout())
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
	g.log.Debugf("Starting")

	g.pingTicker = NewIntervalAwareForceTicker(
		g.timeoutManager.GetPingTime(),
	)
	g.pingTicker.Resume()

	g.pongTicker = NewIntervalAwareForceTicker(
		g.timeoutManager.GetPongTime(),
	)

	g.resendTicker = time.NewTicker(g.timeoutManager.GetResendTimeout())

	g.wg.Add(1)
	go func() {
		defer func() {
			g.wg.Done()
			if err := g.Close(); err != nil {
				g.log.Errorf("Error closing GoBackNConn: %v",
					err)
			}
		}()

		err := g.receivePacketsForever()
		if err != nil {
			g.log.Debugf("Error in receivePacketsForever: %v", err)
		}

		g.log.Debugf("receivePacketsForever stopped")
	}()

	g.wg.Add(1)
	go func() {
		defer func() {
			g.wg.Done()
			if err := g.Close(); err != nil {
				g.log.Errorf("Error closing GoBackNConn: %v",
					err)
			}
		}()

		err := g.sendPacketsForever()
		if err != nil {
			g.log.Debugf("Error in sendPacketsForever: %v", err)

		}

		g.log.Debugf("sendPacketsForever stopped")
	}()
}

// Close attempts to cleanly close the connection by sending a FIN message.
func (g *GoBackNConn) Close() error {
	g.closeOnce.Do(func() {
		g.log.Debugf("Closing GoBackNConn")

		// We close the quit channel to stop the usual operations of the
		// server.
		close(g.quit)

		// Try send a FIN message to the peer if they have not already
		// done so.
		select {
		case <-g.remoteClosed:
		default:
			g.log.Tracef("Try sending FIN")

			ctxc, cancel := context.WithTimeout(
				g.ctx, g.timeoutManager.GetFinSendTimeout(),
			)
			defer cancel()

			err := g.sendPacket(ctxc, &PacketFIN{}, false)
			if err != nil {
				g.log.Errorf("Error sending FIN: %v", err)
			}
		}

		// Canceling the context will ensure that we are not hanging on
		// the receive or send functions passed to the server on
		// initialisation.
		g.cancel()

		g.sendQueue.stop()

		g.wg.Wait()

		if g.pingTicker != nil {
			g.pingTicker.Stop()
		}
		if g.resendTicker != nil {
			g.resendTicker.Stop()
		}

		g.log.Debugf("GBN is closed")
	})

	return nil
}

// sendPacket serializes a message and writes it to the underlying send stream.
func (g *GoBackNConn) sendPacket(ctx context.Context, msg Message,
	isResend bool) error {

	b, err := msg.Serialize()
	if err != nil {
		return fmt.Errorf("serialize error: %s", err)
	}

	err = g.cfg.sendToStream(ctx, b)
	if err != nil {
		return fmt.Errorf("error calling sendToStream: %s", err)
	}

	// Notify the timeout manager that a message has been sent.
	g.timeoutManager.Sent(msg, isResend)

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
		err := g.sendQueue.resend()
		if err != nil {
			return err
		}

		// After resending the queue, we reset the resend ticker.
		// This is so that we don't immediately resend the queue again,
		// if the sendQueue.resend call above took a long time to
		// execute. That can happen if the function was awaiting the
		// expected ACK for a long time, or times out while awaiting the
		// catch up.
		g.resendTicker.Reset(g.timeoutManager.GetResendTimeout())

		// Also drain the resend signal channel, as resendTicker.Reset
		// doesn't drain the channel if the ticker ticked during the
		// sendQueue.resend() call above.
		select {
		case <-g.resendTicker.C:
		default:
		}

		return nil
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
			// If we have expected a sync after sending the previous
			// ping, both the pingTicker and pongTicker may have
			// ticked when waiting to sync. In that case, we can't
			// be sure which of the signals we receive over first in
			// the above select. We therefore need to check if the
			// pong ticker has ticked here to ensure that it get's
			// prioritized over the ping ticker.
			select {
			case <-g.pongTicker.Ticks():
				return errKeepaliveTimeout
			default:
			}

			// Start the pong timer.
			g.pongTicker.Reset()
			g.pongTicker.Resume()

			// Also reset the ping timer.
			g.pingTicker.Reset()

			g.log.Tracef("Sending a PING packet")

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

		g.log.Tracef("Sending data %d", packet.Seq)
		if err := g.sendPacket(g.ctx, packet, false); err != nil {
			return err
		}

		for {
			// If the queue size is still less than N, we can
			// continue to add more packets to the queue.
			if g.sendQueue.size() < g.cfg.n {
				break
			}

			g.log.Tracef("The queue is full.")

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

		b, err := g.cfg.recvFromStream(g.ctx)
		if err != nil {
			return fmt.Errorf("error receiving "+
				"from recvFromStream: %s", err)
		}

		msg, err := Deserialize(b)
		if err != nil {
			return fmt.Errorf("deserialize error: %s", err)
		}

		// Notify the timeout manager that a message has been received.
		g.timeoutManager.Received(msg)

		// Reset the ping & pong timer if any packet is received.
		// If ping/pong is disabled, this is a no-op.
		g.pingTicker.Reset()
		if g.pongTicker.IsActive() {
			g.pongTicker.Pause()
		}

		g.resendTicker.Reset(g.timeoutManager.GetResendTimeout())

		switch m := msg.(type) {
		case *PacketData:
			switch m.Seq == g.recvSeq {
			case true:
				// We received a data packet with the sequence
				// number we were expecting. So we respond with
				// an ACK message with that sequence number
				// and we bump the sequence number that we
				// expect of the next data packet.
				g.log.Tracef("Got expected data %d", m.Seq)

				ack := &PacketACK{
					Seq: m.Seq,
				}

				err = g.sendPacket(g.ctx, ack, false)
				if err != nil {
					return err
				}

				g.recvSeq = (g.recvSeq + 1) % g.cfg.s

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
				g.log.Tracef("Got unexpected data %d", m.Seq)

				// If we recently sent a NACK for the same
				// sequence number then back off.
				// We wait 2 times the resendTimeout before
				// sending a new nack, as this case is likely
				// hit if the sender is currently resending
				// the queue, and therefore the threads that
				// are resending the queue is likely busy with
				// the resend, and therefore won't react to the
				// NACK we send here in time.
				sinceSent := time.Since(lastNackTime)

				timeout := g.timeoutManager.GetResendTimeout()
				recentlySent := sinceSent < timeout*2

				if lastNackSeq == g.recvSeq && recentlySent {
					g.log.Tracef("Recently sent NACK")

					continue
				}

				g.log.Tracef("Sending NACK %d", g.recvSeq)

				// Send a NACK with the expected sequence
				// number.
				nack := &PacketNACK{
					Seq: g.recvSeq,
				}

				err = g.sendPacket(g.ctx, nack, false)
				if err != nil {
					return err
				}

				lastNackTime = time.Now()
				lastNackSeq = nack.Seq
			}

		case *PacketACK:
			gotValidACK := g.sendQueue.processACK(m.Seq)
			if gotValidACK {
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
			shouldResend, bumped := g.sendQueue.processNACK(m.Seq)

			// If we don't need to resend the queue after processing
			// the NACK, we can continue without sending the resend
			// signal.
			if !shouldResend {
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

			g.log.Tracef("Sending a resend signal")

			// Send a signal to indicate that new sends should pause
			// and the current queue should be resent instead.
			select {
			case g.resendSignal <- struct{}{}:
			default:
			}

		case *PacketFIN:
			// A FIN packet indicates that the peer would like to
			// close the connection.
			g.log.Tracef("Received a FIN packet")

			close(g.remoteClosed)

			if g.cfg.onFIN != nil {
				g.cfg.onFIN()
			}

			return errTransportClosing

		default:
			return fmt.Errorf("received unexpected message: %T",
				msg)
		}
	}
}
