package gbn

import (
	"sync"
	"time"
)

// queue is a fixed size queue with a sliding window that has a base and a top
// modulo s.
type queue struct {
	// content is the current content of the queue. This is always a slice
	// of length s but can contain nil elements if the queue isn't full.
	content []*PacketData

	// s is the maximum sequence number used to label packets. Packets
	// are labelled with incrementing sequence numbers modulo s.
	// s must be strictly larger than the window size, n. This
	// is so that the receiver can tell if the sender is resending the
	// previous window (maybe the sender did not receive the acks) or if
	// they are sending the next window. If s <= n then there would be
	// no way to tell.
	s uint8

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

	lastResend       time.Time
	handshakeTimeout time.Duration
}

// newQueue creates a new queue.
func newQueue(s uint8, handshakeTimeout time.Duration) *queue {
	return &queue{
		content:          make([]*PacketData, s),
		s:                s,
		handshakeTimeout: handshakeTimeout,
	}
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

	return q.sequenceTop + (q.s - q.sequenceBase)
}

// addPacket adds a new packet to the queue.
func (q *queue) addPacket(packet *PacketData) {
	q.topMtx.Lock()
	defer q.topMtx.Unlock()

	packet.Seq = q.sequenceTop
	q.content[q.sequenceTop] = packet
	q.sequenceTop = (q.sequenceTop + 1) % q.s
}

// resend invokes the callback for each packet that needs to be re-sent.
func (q *queue) resend(cb func(packet *PacketData) error) error {
	if time.Since(q.lastResend) < q.handshakeTimeout {
		log.Tracef("Resent the queue recently.")

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

	log.Tracef("Resending the queue")

	for base != top {
		packet := q.content[base]

		if err := cb(packet); err != nil {
			return err
		}
		base = (base + 1) % q.s

		log.Tracef("Resent %d", packet.Seq)
	}

	return nil
}

// processACK processes an incoming ACK of a given sequence number.
func (q *queue) processACK(seq uint8) bool {

	// If our queue is empty, an ACK should not have any effect.
	if q.size() == 0 {
		log.Tracef("Received ack %d, but queue is empty. Ignoring.",
			seq)
		return false
	}

	q.baseMtx.Lock()
	defer q.baseMtx.Unlock()

	if seq == q.sequenceBase {
		// We received an ACK packet with the sequence number that is
		// equal to the one we were expecting. So we increase our base
		// accordingly and send a signal to indicate that the queue size
		// has decreased.
		log.Tracef("Received correct ack %d", seq)

		q.sequenceBase = (q.sequenceBase + 1) % q.s

		// We did receive an ACK.
		return true
	}

	// We received an ACK with a sequence number that we were not expecting.
	// This could be a duplicate ACK before or it could be that we just
	// missed the ACK for the current base and this is actually an ACK for
	// another packet in the queue.
	log.Tracef("Received wrong ack %d, expected %d", seq, q.sequenceBase)

	q.topMtx.RLock()
	defer q.topMtx.RUnlock()

	// If this is an ACK for something in the current queue then maybe we
	// just missed a previous ACK. We can bump the base to be equal to this
	// sequence number.
	if containsSequence(q.sequenceBase, q.sequenceTop, seq) {
		log.Tracef("Sequence %d is in the queue. Bump the base.", seq)

		q.sequenceBase = (seq + 1) % q.s

		// We did receive an ACK.
		return true
	}

	// We didn't receive a valid ACK for anything in our queue.
	return false
}

// processNACK processes an incoming NACK of a given sequence number.
func (q *queue) processNACK(seq uint8) (bool, bool) {
	q.baseMtx.Lock()
	defer q.baseMtx.Unlock()

	q.topMtx.RLock()
	defer q.topMtx.RUnlock()

	log.Tracef("Received NACK %d", seq)

	// If the NACK is the same as sequenceTop, it probably means that queue
	// was sent successfully, but we just missed the necessary ACKs. So we
	// can empty the queue here by bumping the base and we dont need to
	// trigger a resend.
	if seq == q.sequenceTop {
		q.sequenceBase = q.sequenceTop
		return true, false
	}

	// Is the NACKed sequence even in our queue?
	if !containsSequence(q.sequenceBase, q.sequenceTop, seq) {
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
