package gbn

import (
	"sync"
	"time"

	"github.com/btcsuite/btclog"
)

const (
	// awaitingTimeoutMultiplier defines the multiplier we use when
	// multiplying the resend timeout, resulting in the duration we wait for
	// the sync to be complete before timing out.
	// We set this to 3X the resend timeout. The reason we wait exactly 3X
	// the resend timeout is that we expect that the max time that the
	// correct behavior would take, would be:
	// * 1X the resendTimeout for the time it would take for the party
	// respond with an ACK for the last packet in the resend queue, i.e. the
	// expectedACK.
	// * 1X the resendTimeout while waiting in proceedAfterTime before
	// completing the sync.
	// * 1X extra resendTimeout as buffer, to ensure that we have enough
	// time to process the ACKS/NACKS by other party + some extra margin.
	awaitingTimeoutMultiplier = 3
)

type syncState uint8

const (
	// syncStateIdle is the state representing that the syncer is idle and
	// has not yet initiated a resend sync.
	syncStateIdle syncState = iota

	// syncStateResending is the state representing that the syncer has
	// initiated a resend sync, and is awaiting that the sync is completed.
	syncStateResending
)

// syncer is used to ensure that both the sender and the receiver are in sync
// before the waitForSync function is completed. This is done by waiting until
// we receive either the expected ACK or NACK after resending the queue.
//
// To understand why we need to wait for the expected ACK/NACK after resending
// the queue, it ensures that we don't end up in a situation where we resend the
// queue over and over again due to latency and delayed NACKs by the other
// party.
//
// Consider the following scenario:
// 1.
// Alice sends packets 1, 2, 3 & 4 to Bob.
// 2.
// Bob receives packets 1, 2, 3 & 4, and sends back the respective ACKs.
// 3.
// Alice receives ACKs for packets 1 & 2, but due to latency the ACKs for
// packets 3 & 4 are delayed and aren't received until Alice resend timeout
// has passed, which leads to Alice resending packets 3 & 4. Alice will after
// that receive the delayed ACKs for packets 3 & 4, but will consider that as
// the ACKs for the resent packets, and not the original packets which they were
// actually sent for. If we didn't wait after resending the queue, Alice would
// then proceed to send more packets (5 & 6).
// 4.
// When Bob receives the resent packets 3 & 4, Bob will respond with NACK 5. Due
// to latency, the packets 5 & 6 that Alice sent in step (3) above will then be
// received by Bob, and be processed as the correct response to the NACK 5. Bob
// will after that await packet 7.
// 5.
// Alice will receive the NACK 5, and now resend packets 5 & 6. But as Bob is
// now awaiting packet 7, this send will lead to a NACK 7. But due to latency,
// if Alice doesn't wait resending the queue, Alice will proceed to send new
// packet(s) before receiving the NACK 7.
// 6.
// This resend loop would continue indefinitely, so we need to ensure that Alice
// waits after she has resent the queue, to ensure that she doesn't proceed to
// send new packets before she is sure that both parties are in sync.
//
// To ensure that we are in sync, after we have resent the queue, we will await
// that we either:
// 1. Receive a NACK for the sequence number succeeding the last packet in the
// resent queue i.e. in step (3) above, that would be NACK 5.
// OR
// 2. Receive an ACK for the last packet in the resent queue i.e. in step (3)
// above, that would be ACK 4. After we receive the expected ACK, we will then
// wait for the duration of the resend timeout before continuing. The reason why
// we wait for the resend timeout before continuing, is that the ACKs we are
// getting after a resend, could be delayed ACKs for the original packets we
// sent, and not ACKs for the resent packets. In step (3) above, the ACKs for
// packets 3 & 4 that Alice received were delayed ACKs for the original packets.
// If Alice would have immediately continued to send new packets (5 & 6) after
// receiving the ACK 4, she would have then received the NACK 5 from Bob which
// was the actual response to the resent queue. But as Alice had already
// continued to send packets 5 & 6 when receiving the NACK 5, the resend queue
// response to that NACK would cause the resend loop to continue indefinitely.
// OR
// 3. If neither of condition 1 or 2 above is met within 3X the resend timeout,
// we will time out and consider the sync to be completed. See the docs for
// awaitingTimeoutMultiplier for more details on why we wait 3X the resend
// timeout.
//
// When either of the 3 conditions above are met, we will consider both parties
// to be in sync.
type syncer struct {
	s              uint8
	log            btclog.Logger
	timeoutManager *TimeoutManager

	state syncState

	// expectedACK defines the sequence number for the last packet in the
	// resend queue. If we receive an ACK for this sequence number while
	// waiting to sync, we wait for the duration of the resend timeout,
	// and then proceed to send new packets, unless we receive the
	// expectedNACK during the wait time. If that happens, we will proceed
	// to send new packets as soon as we have processed the NACK.
	expectedACK uint8

	// expectedNACK is set to the sequence number that follows the last item
	// in resend queue, when a sync is initiated. In case we get a NACK with
	// this sequence number when waiting to sync, we'd consider the sync to
	// be completed and we can proceed to send new packets.
	expectedNACK uint8

	// cancel is used to mark that the sync has been completed.
	cancel chan struct{}

	quit chan struct{}
	mu   sync.Mutex
}

// newSyncer creates a new syncer instance.
func newSyncer(s uint8, prefixLogger btclog.Logger,
	timeoutManager *TimeoutManager, quit chan struct{}) *syncer {

	if prefixLogger == nil {
		prefixLogger = log
	}

	return &syncer{
		s:              s,
		log:            prefixLogger,
		timeoutManager: timeoutManager,
		state:          syncStateIdle,
		cancel:         make(chan struct{}),
		quit:           quit,
	}
}

// reset resets the syncer state to idle and marks the sync as completed.
func (c *syncer) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.resetUnsafe()
}

// resetUnsafe resets the syncer state to idle and marks the sync as completed.
//
// NOTE: when calling this function, the caller must hold the syncer mutex.
func (c *syncer) resetUnsafe() {
	c.state = syncStateIdle

	// Cancel any pending sync.
	select {
	case c.cancel <- struct{}{}:
	default:
	}
}

// initResendUpTo initializes the syncer to the resending state, and will after
// this call be ready to wait for the sync to be completed when calling the
// waitForSync function.
// The top argument defines the sequence number of the next packet to be sent
// after resending the queue.
func (c *syncer) initResendUpTo(top uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.state = syncStateResending

	// Drain the cancel channel, to reinitialize it for the new sync.
	select {
	case <-c.cancel:
	default:
	}

	c.expectedACK = (c.s + top - 1) % c.s
	c.expectedNACK = top

	c.log.Tracef("Set expectedACK to %d & expectedNACK to %d",
		c.expectedACK, c.expectedNACK)
}

// getState returns the current state of the syncer.
func (c *syncer) getState() syncState {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.state
}

// waitForSync waits for the sync to be completed. The sync is completed when we
// receive either the expectedNACK, the expectedACK + resend timeout has passed,
// or when timing out.
func (c *syncer) waitForSync() {
	c.log.Tracef("Awaiting sync after resending the queue")

	select {
	case <-c.quit:
		return

	case <-c.cancel:
		c.log.Tracef("sync canceled or reset")

	case <-time.After(
		c.timeoutManager.GetResendTimeout() * awaitingTimeoutMultiplier,
	):
		c.log.Tracef("Timed out while waiting for sync")
	}

	c.reset()
}

// processACK marks the sync as completed if the passed sequence number matches
// the expectedACK, after the resend timeout has passed.
// If we are not resending or waiting after a resend, this is a no-op.
func (c *syncer) processACK(seq uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If we are not resending or waiting after a resend, just swallow the
	// ACK.
	if c.state != syncStateResending {
		return
	}

	// Else, if we are waiting but this is not the ack we are waiting for,
	// just swallow it.
	if seq != c.expectedACK {
		return
	}

	c.log.Tracef("Got expected ACK")

	// We start the proceedAfterTime function in a goroutine, as we
	// don't want to block the processing of other NACKs/ACKs while
	// we're waiting for the resend timeout to expire.
	go c.proceedAfterTime()
}

// processNACK marks the sync as completed if the passed sequence number matches
// the expectedNACK.
// If we are not resending or waiting after a resend, this is a no-op.
func (c *syncer) processNACK(seq uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If we are not resending or waiting after a resend, just swallow the
	// NACK.
	if c.state != syncStateResending {
		return
	}

	// Else, if we are waiting but this is not the NACK we are waiting for,
	// just swallow it.
	if seq != c.expectedNACK {
		return
	}

	c.log.Tracef("Got expected NACK")

	c.resetUnsafe()
}

// proceedAfterTime will wait for the resendTimeout and then complete the sync,
// if we haven't completed the sync yet by receiving the expectedNACK.
func (c *syncer) proceedAfterTime() {
	// We await for the duration of the resendTimeout before completing the
	// sync, as that's the time we'd expect it to take for the other party
	// to respond with a NACK, if the resent last packet in the
	// queue would lead to a NACK. If we receive the expectedNACK
	// before the timeout, the cancel channel will be sent over, and we can
	// stop the execution early.
	select {
	case <-c.quit:
		return

	case <-c.cancel:
		c.log.Tracef("sync succeeded or was reset")

		// As we can't be sure that waitForSync cancel listener was
		// triggered before this one, we send over the cancel channel
		// again, to make sure that both listeners are triggered.
		c.reset()

		return

	case <-time.After(c.timeoutManager.GetResendTimeout()):
		c.mu.Lock()
		defer c.mu.Unlock()

		if c.state != syncStateResending {
			return
		}

		c.log.Tracef("Completing sync after expectedACK timeout")

		c.resetUnsafe()
	}
}
