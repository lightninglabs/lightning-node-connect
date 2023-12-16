package gbn

import (
	"sync"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/stretchr/testify/require"
)

// TestSyncer tests that the syncer functionality works as expected. It
// specifically tests that we wait the expected durations for cases when we
// initiate resends and 1) don't receive the expected ACK/NACK, 2) receive the
// expected ACK, and 3) receive the expected NACK.
func TestSyncer(t *testing.T) {
	t.Parallel()

	syncTimeout := time.Second * 1
	expectedNACK := uint8(3)

	tm := NewTimeOutManager(nil)
	tm.resendTimeout = syncTimeout

	syncer := newSyncer(5, nil, tm, make(chan struct{}))

	// Let's first test the scenario where we don't receive the expected
	// ACK/NACK after initiating the resend. This should trigger a timeout
	// of the sync, which should be the:
	// syncTimeout * awaitingTimeoutMultiplier.
	startTime := time.Now()

	var wg sync.WaitGroup
	initResend(t, syncer, &wg, expectedNACK)

	wg.Wait()

	// Check that the syncing took at least the
	// syncTimeout * awaitingTimeoutMultiplier to complete.
	require.GreaterOrEqual(
		t, time.Since(startTime),
		syncTimeout*awaitingTimeoutMultiplier,
	)

	// Now let's test the scenario where we do receive the expected ACK for
	// the when awaiting the sync. This should trigger the
	// syncer.proceedAfterTime call, which should finish complete the sync
	// after the syncTimeout.
	startTime = time.Now()

	initResend(t, syncer, &wg, expectedNACK)

	// Simulate that we receive the expected ACK.
	simulateACKProcessing(t, syncer, 1, expectedNACK-1)

	wg.Wait()

	// Now check that the sync took at least the syncTimeout to complete.
	require.GreaterOrEqual(t, time.Since(startTime), syncTimeout)
	require.LessOrEqual(t, time.Since(startTime), syncTimeout*2)

	// Finally, let's test the scenario where we do receive the expected
	// NACK when syncing. This completes the sync immediately.
	startTime = time.Now()

	initResend(t, syncer, &wg, expectedNACK)

	// Simulate that we receive the expected NACK.
	syncer.processNACK(expectedNACK)

	wg.Wait()

	// Finally let's check that we didn't exit on the timeout in this case.
	require.Less(t, time.Since(startTime), syncTimeout)
}

// simulateACKProcessing is a helper function that simulates the processing of
// ACKs for the given range of sequence numbers.
func simulateACKProcessing(t *testing.T, syncer *syncer, base, last uint8) {
	t.Helper()

	for i := base; i <= last; i++ {
		syncer.processACK(i)
	}
}

// initResend is a helper function that triggers an initResendUpTo for the given
// top and then waits for the sync to complete in a goroutine, and notifies
// the WaitGroup when the sync has completed.
// The function will block until the initResendUpTo function has executed.
func initResend(t *testing.T, syncer *syncer, wg *sync.WaitGroup, top uint8) {
	t.Helper()

	require.Equal(t, syncStateIdle, syncer.getState())

	wg.Add(1)

	// This will trigger a resend catchup, so we launch the resend in a
	// goroutine.
	go func() {
		syncer.initResendUpTo(top)
		syncer.waitForSync()

		wg.Done()
	}()

	// We also ensure that the above goroutine has executed the
	// initResendUpTo function before this function returns.
	err := wait.Predicate(func() bool {
		return syncer.getState() == syncStateResending
	}, time.Second)
	require.NoError(t, err)
}
