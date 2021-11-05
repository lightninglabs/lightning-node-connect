package gbn

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/lightningnetwork/lnd/ticker"
)

// IntervalAwareForceTicker implements the Ticker interface, and provides a
// method of force-feeding ticks, even while paused. This is a copy of lnd's
// ticker.Force that is also aware when the last timed tick happened and how
// long approximately it takes until the next timed tick happens.
type IntervalAwareForceTicker struct {
	isActive uint32 // used atomically

	// Force is used to force-feed a ticks into the ticker. Useful for
	// debugging when trying to wake an event.
	Force chan time.Time

	ticker *time.Ticker
	skip   chan struct{}

	interval time.Duration

	// lastTimedTick is the timestamp when the last tick occurred that was
	// fired by the underlying clock. This does not mean that the tick was
	// necessarily also forwarded to the Force channel. If we are paused,
	// this timestamp is still updated but no ticks are sent to the channel.
	lastTimedTick    time.Time
	lastTimedTickMtx sync.Mutex

	wg   sync.WaitGroup
	quit chan struct{}
}

// A compile-time constraint to ensure IntervalAwareForceTicker satisfies the
// ticker.Ticker interface.
var _ ticker.Ticker = (*IntervalAwareForceTicker)(nil)

// NewIntervalAwareForceTicker returns a IntervalAwareForceTicker ticker, used
// for testing and debugging. It supports the ability to force-feed events that
// get output by the channel returned by Ticks().
func NewIntervalAwareForceTicker(interval time.Duration) *IntervalAwareForceTicker {
	t := &IntervalAwareForceTicker{
		ticker:        time.NewTicker(interval),
		interval:      interval,
		Force:         make(chan time.Time),
		skip:          make(chan struct{}),
		quit:          make(chan struct{}),
		lastTimedTick: time.Now(),
	}

	// Start the main goroutine that handles the clock ticks.
	t.start()

	return t
}

// start starts the main event loop where real ticks are proxied to our Force
// channel if we are active.
func (t *IntervalAwareForceTicker) start() {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			select {
			case tick := <-t.ticker.C:
				// Always update the last tick timestamp so we
				// can more accurately say when the next one
				// will happen if we're un-paused.
				t.lastTimedTickMtx.Lock()
				t.lastTimedTick = time.Now()
				t.lastTimedTickMtx.Unlock()

				if !t.IsActive() {
					continue
				}

				select {
				case t.Force <- tick:
				case <-t.skip:
				case <-t.quit:
					return
				}

			case <-t.quit:
				return
			}
		}
	}()
}

// Ticks returns a receive-only channel that delivers times at the ticker's
// prescribed interval when active. Force-fed ticks can be delivered at any
// time.
//
// NOTE: Part of the Ticker interface.
func (t *IntervalAwareForceTicker) Ticks() <-chan time.Time {
	return t.Force
}

// Resume starts underlying time.Ticker and causes the ticker to begin
// delivering scheduled events.
//
// NOTE: Part of the Ticker interface.
func (t *IntervalAwareForceTicker) Resume() {
	atomic.StoreUint32(&t.isActive, 1)
}

// Pause suspends the underlying ticker, such that Ticks() stops signaling at
// regular intervals.
//
// NOTE: Part of the Ticker interface.
func (t *IntervalAwareForceTicker) Pause() {
	atomic.StoreUint32(&t.isActive, 0)

	// If the ticker fired and read isActive as true, it may still send the
	// tick. We'll try to send on the skip channel to drop it.
	select {
	case t.skip <- struct{}{}:
	default:
	}
}

// Stop suspends the underlying ticker, such that Ticks() stops signaling at
// regular intervals, and permanently frees up any resources.
//
// NOTE: Part of the Ticker interface.
func (t *IntervalAwareForceTicker) Stop() {
	t.Pause()
	t.ticker.Stop()
	close(t.quit)
	t.wg.Wait()
}

// ResetWithInterval restarts the ticker with the given interval, causing the
// next clock tick to occur in the given interval.
func (t *IntervalAwareForceTicker) ResetWithInterval(newInterval time.Duration) {
	// Shutdown the internal clock ticker without changing isActive.
	t.ticker.Stop()
	close(t.quit)
	t.wg.Wait()

	// Reset everything to the same state as if we'd just created this
	// ticker from scratch.
	t.interval = newInterval
	t.ticker = time.NewTicker(newInterval)
	t.quit = make(chan struct{})
	t.lastTimedTickMtx.Lock()
	t.lastTimedTick = time.Now()
	t.lastTimedTickMtx.Unlock()

	// Restart the actual run loop now that we have a new ticker.
	t.start()
}

// Reset restarts the ticker interval, causing the next clock tick to occur in
// the configured interval.
func (t *IntervalAwareForceTicker) Reset() {
	t.ResetWithInterval(t.interval)
}

// ForceTick force feeds an event into the ticker channel and resets the
// internal clock ticker causing the next clock tick to occur in the configured
// interval.
func (t *IntervalAwareForceTicker) ForceTick() {
	t.Reset()
	t.Force <- time.Now()
}

// LastTimedTick returns the timestamp when the last tick occurred that was
// fired by the underlying clock. This does not mean that the tick was
// necessarily also forwarded to the Force channel. If we are paused,
// this timestamp is still updated but no ticks are sent to the channel.
func (t *IntervalAwareForceTicker) LastTimedTick() time.Time {
	t.lastTimedTickMtx.Lock()
	defer t.lastTimedTickMtx.Unlock()
	return t.lastTimedTick
}

// NextTickIn returns the approximate duration until the next timed tick will
// occur.
func (t *IntervalAwareForceTicker) NextTickIn() time.Duration {
	nextTick := t.LastTimedTick().Add(t.interval)
	durationToNextTick := time.Until(nextTick)
	if durationToNextTick < 0 {
		return 0
	}
	return durationToNextTick
}

// IsActive returns true if the timed ticks are currently forwarded to the Force
// channel.
func (t *IntervalAwareForceTicker) IsActive() bool {
	return atomic.LoadUint32(&t.isActive) == 1
}
