package gbn

import (
	"sync"
	"time"

	"github.com/btcsuite/btclog"
)

type queueCfg struct {
	// s is the maximum sequence number used to label packets. Packets
	// are labelled with incrementing sequence numbers modulo s.
	// s must be strictly larger than the window size, n. This
	// is so that the receiver can tell if the sender is resending the
	// previous window (maybe the sender did not receive the acks) or if
	// they are sending the next window. If s <= n then there would be
	// no way to tell.
	s uint8

	log btclog.Logger

	sendPkt func(packet *PacketData) error
}

// queue is a fixed size queue with a sliding window that has a base and a top
// modulo s.
type queue struct {
	cfg *queueCfg

	timeoutManager *TimeoutManager

	// content is the current content of the queue. This is always a slice
	// of length s but can contain nil elements if the queue isn't full.
	content []*PacketData

	// sequenceBase keeps track of the base of the send window and so
	// represents the next ack that we expect from the receiver. The
	// maximum value of sequenceBase is s.
	// sequenceBase must be guarded by baseMtx.
	sequenceBase uint8

	// baseMtx is used to guard sequenceBase.
	baseMtx sync.RWMutex

	// sequenceTop is the sequence number of the latest packet.
	// The difference between sequenceTop and sequenceBase should never
	// exceed the window size, n. The maximum value of sequenceBase is s.
	// sequenceTop must be guarded by topMtx.
	sequenceTop uint8

	// topMtx is used to guard sequenceTop.
	topMtx sync.RWMutex

	syncer *syncer

	lastResend time.Time

	quit chan struct{}
}

// newQueue creates a new queue.
func newQueue(cfg *queueCfg, timeoutManager *TimeoutManager) *queue {
	if cfg.log == nil {
		cfg.log = log
	}

	q := &queue{
		cfg:            cfg,
		content:        make([]*PacketData, cfg.s),
		quit:           make(chan struct{}),
		timeoutManager: timeoutManager,
	}

	q.syncer = newSyncer(cfg.s, cfg.log, timeoutManager, q.quit)

	return q
}

func (q *queue) stop() {
	close(q.quit)
}

// size is used to calculate the current sender queueSize.
func (q *queue) size() uint8 {
	q.baseMtx.RLock()
	defer q.baseMtx.RUnlock()

	q.topMtx.RLock()
	defer q.topMtx.RUnlock()

	if q.sequenceTop >= q.sequenceBase {
		return q.sequenceTop - q.sequenceBase
	}

	return q.sequenceTop + (q.cfg.s - q.sequenceBase)
}

// addPacket adds a new packet to the queue.
func (q *queue) addPacket(packet *PacketData) {
	q.topMtx.Lock()
	defer q.topMtx.Unlock()

	packet.Seq = q.sequenceTop
	q.content[q.sequenceTop] = packet
	q.sequenceTop = (q.sequenceTop + 1) % q.cfg.s
}

// resend resends the current contents of the queue. It allows some time for the
// two parties to be seen as synced; this may fail in which case the caller is
// expected to call resend again.
func (q *queue) resend() error {
	if time.Since(q.lastResend) < q.timeoutManager.GetHandshakeTimeout() {
		q.cfg.log.Tracef("Resent the queue recently.")

		return nil
	}

	if q.size() == 0 {
		return nil
	}

	q.lastResend = time.Now()

	q.baseMtx.RLock()
	base := q.sequenceBase
	q.baseMtx.RUnlock()

	q.topMtx.RLock()
	top := q.sequenceTop
	q.topMtx.RUnlock()

	if base == top {
		return nil
	}

	// Prepare the queue for awaiting the resend catch up.
	q.syncer.initResendUpTo(top)

	q.cfg.log.Tracef("Resending the queue")

	for base != top {
		packet := q.content[base]

		if err := q.cfg.sendPkt(packet); err != nil {
			return err
		}

		base = (base + 1) % q.cfg.s

		q.cfg.log.Tracef("Resent %d", packet.Seq)
	}

	// Then wait until we know that both parties are in sync.
	q.syncer.waitForSync()

	return nil
}

// processACK processes an incoming ACK of a given sequence number. The function
// returns true if the passed seq is an ACK for a packet we have sent but not
// yet received an ACK for.
func (q *queue) processACK(seq uint8) bool {
	// If our queue is empty, an ACK should not have any effect.
	if q.size() == 0 {
		q.cfg.log.Tracef("Received ack %d, but queue is empty. "+
			"Ignoring.", seq)

		return false
	}

	q.syncer.processACK(seq)

	q.baseMtx.Lock()
	defer q.baseMtx.Unlock()

	if seq == q.sequenceBase {
		// We received an ACK packet with the sequence number that is
		// equal to the one we were expecting. So we increase our base
		// accordingly and send a signal to indicate that the queue size
		// has decreased.
		q.cfg.log.Tracef("Received correct ack %d", seq)

		q.sequenceBase = (q.sequenceBase + 1) % q.cfg.s

		// We did receive an ACK.
		return true
	}

	// We received an ACK with a sequence number that we were not expecting.
	// This could be a duplicate ACK before or it could be that we just
	// missed the ACK for the current base and this is actually an ACK for
	// another packet in the queue.
	q.cfg.log.Tracef("Received wrong ack %d, expected %d", seq,
		q.sequenceBase)

	q.topMtx.RLock()
	defer q.topMtx.RUnlock()

	// If this is an ACK for something in the current queue then maybe we
	// just missed a previous ACK. We can bump the base to be equal to this
	// sequence number.
	if containsSequence(q.sequenceBase, q.sequenceTop, seq) {
		q.cfg.log.Tracef("Sequence %d is in the queue. Bump the base.",
			seq)

		q.sequenceBase = (seq + 1) % q.cfg.s

		// We did receive an ACK.
		return true
	}

	// We didn't receive a valid ACK for anything in our queue.
	return false
}

// processNACK processes an incoming NACK of a given sequence number.
// The function returns two booleans. The first boolean is set to true if we
// should resend the queue after processing the NACK. The second boolean is set
// to true if the NACK sequence number is a packet in the queue which isn't the
// queue base, and we therefore don't need to resend any of the packets before
// the NACK sequence number. This equivalent to receiving the ACKs for the
// packets before the NACK sequence number.
func (q *queue) processNACK(seq uint8) (bool, bool) {
	q.baseMtx.Lock()
	defer q.baseMtx.Unlock()

	q.topMtx.RLock()
	defer q.topMtx.RUnlock()

	q.cfg.log.Tracef("Received NACK %d", seq)

	q.syncer.processNACK(seq)

	// If the NACK is the same as sequenceTop, it probably means that queue
	// was sent successfully, but due to latency we timed out and resent the
	// queue before we received the ACKs for the sent packages.
	// Alternatively, we might have just missed the necessary ACKs. So we
	// can empty the queue here by bumping the base and we don't need to
	// trigger a resend.
	if seq == q.sequenceTop {
		q.sequenceBase = q.sequenceTop

		return false, true
	}

	// Is the NACKed sequence even in our queue?
	if !containsSequence(q.sequenceBase, q.sequenceTop, seq) {
		q.cfg.log.Tracef("NACK seq %d is not in the queue. Ignoring.",
			seq)

		return false, false
	}

	// The NACK sequence is in the queue. So we bump the
	// base to be whatever the sequence is.
	bumped := false
	if q.sequenceBase != seq {
		bumped = true
	}

	q.sequenceBase = seq

	return true, bumped
}

// containsSequence is used to determine if a number, seq, is between two other
// numbers, base and top, where all the numbers lie in a finite field (modulo
// space) s.
func containsSequence(base, top, seq uint8) bool {
	// If base and top are equal then the queue is empty.
	if base == top {
		return false
	}

	if base < top {
		if base <= seq && seq < top {
			return true
		}
		return false
	}

	// top < base
	if seq < top || base <= seq {
		return true
	}

	return false
}
