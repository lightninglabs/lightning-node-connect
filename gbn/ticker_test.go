package gbn_test

import (
	"math"
	"testing"
	"time"

	"github.com/lightninglabs/lightning-node-connect/gbn"
)

const (
	interval           = 100 * time.Millisecond
	numActiveTicks     = 3
	timestampTolerance = 10 * time.Millisecond
)

var tickers = []struct {
	name   string
	ticker *gbn.IntervalAwareForceTicker
}{
	{
		"interval aware ticker",
		gbn.NewIntervalAwareForceTicker(interval),
	},
}

// TestTickers verifies that both our production and mock tickers exhibit the
// same principle behaviors when accessed via the ticker.Ticker interface
// methods.
func TestInterfaceTickers(t *testing.T) {
	for _, test := range tickers {
		test := test
		t.Run(test.name, func(t *testing.T) {
			testTicker(t, test.ticker)
		})
	}
}

// testTicker asserts the behavior of a freshly initialized ticker.Ticker.
func testTicker(t *testing.T, ticker *gbn.IntervalAwareForceTicker) {
	// Newly initialized ticker should start off inactive.
	select {
	case <-ticker.Ticks():
		t.Fatalf("ticker should not have ticked before calling Resume")
	case <-time.After(2 * interval):
	}

	// Resume, ticker should be active and start sending ticks.
	ticker.Resume()

	for i := 0; i < numActiveTicks; i++ {
		select {
		case <-ticker.Ticks():
			assertTickTimeUpdated(t, ticker)
		case <-time.After(2 * interval):
			t.Fatalf("ticker should have ticked after calling " +
				"Resume")
		}
	}

	// Make sure the time to next tick is calculated properly.
	time.Sleep(interval - timestampTolerance)
	nextTickIn := ticker.NextTickIn()
	if nextTickIn > timestampTolerance {
		t.Fatalf("expected next tick to be in %v but was %v",
			timestampTolerance, nextTickIn)
	}

	// Wait for next tick to get synced up with the underlying clock for the
	// next check.
	<-ticker.Ticks()

	// Pause, check that ticker is inactive and sends no ticks.
	ticker.Pause()

	select {
	case <-ticker.Ticks():
		t.Fatalf("ticker should not have ticked after calling Pause")
	case <-time.After(2 * interval):
		// The underlying ticker still should've just ticked.
		assertTickTimeUpdated(t, ticker)
	}

	// Pause again, expect same behavior as after first invocation.
	ticker.Pause()

	select {
	case <-ticker.Ticks():
		t.Fatalf("ticker should not have ticked after calling Pause " +
			"again")
	case <-time.After(2 * interval):
	}

	// A forced tick should still go through.
	go func() {
		ticker.Force <- time.Now()
	}()

	select {
	case <-ticker.Ticks():
	case <-time.After(2 * interval):
		t.Fatalf("ticker should have fired on forced tick")
	}

	// Resume again, should result in normal active behavior.
	ticker.Resume()

	for i := 0; i < numActiveTicks; i++ {
		select {
		case <-ticker.Ticks():
		case <-time.After(2 * interval):
			t.Fatalf("ticker should have ticked after calling " +
				"Resume")
		}
	}

	// Make sure we can reset the ticker, causing it to restart its internal
	// clock ticker.
	ticker.Reset()
	assertTickTimeUpdated(t, ticker)

	for i := 0; i < numActiveTicks; i++ {
		select {
		case <-ticker.Ticks():
		case <-time.After(2 * interval):
			t.Fatalf("ticker should have ticked after calling " +
				"Reset")
		}
	}

	// Stop the ticker altogether, should render it inactive.
	ticker.Stop()

	select {
	case <-ticker.Ticks():
		t.Fatalf("ticker should not have ticked after calling Stop")
	case <-time.After(2 * interval):
	}
}

func assertTickTimeUpdated(t *testing.T, ticker *gbn.IntervalAwareForceTicker) {
	t.Helper()

	lastTick := ticker.LastTimedTick()
	diffToTarget := time.Until(lastTick)

	// It could be that the ticker is just about to tick. If that's the
	// case, we sleep to allow it to update the last timed tick.
	if float64(diffToTarget) < -float64(interval-timestampTolerance) {
		time.Sleep(timestampTolerance / 2)

		lastTick = ticker.LastTimedTick()
		diffToTarget = time.Until(lastTick)
	}

	if math.Abs(float64(diffToTarget)) > float64(timestampTolerance) {
		t.Fatalf("Expected last tick %v to be within %v of tolerance "+
			"of %v but was %v", lastTick, timestampTolerance,
			time.Now(), diffToTarget)
	}

	nextTickDiff := ticker.NextTickIn() - interval
	if math.Abs(float64(nextTickDiff)) > float64(timestampTolerance) {
		t.Fatalf("Expected next tick to be similar to interval")
	}
}
