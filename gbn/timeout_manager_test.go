package gbn

import (
	"sync"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/stretchr/testify/require"
)

// BenchmarkTimeoutMgrSynchronously benchmarks the timeout manager when sending
// and receiving messages synchronously.
func BenchmarkTimeoutMgrSynchronously(b *testing.B) {
	// Create a new timeout manager to use for the test. We set the timeout
	// update frequency 2, so that the resend timeout is dynamically set
	// every other message.
	tm := NewTimeOutManager(nil, WithTimeoutUpdateFrequency(2))

	for n := 0; n < b.N; n++ {
		msg := &PacketData{Seq: uint8(n)}

		tm.Sent(msg, false)
		tm.Received(msg)
	}
}

// BenchmarkTimeoutMgrConcurrently benchmarks the timeout manager when sending
// and receiving messages concurrently.
func BenchmarkTimeoutMgrConcurrently(b *testing.B) {
	// Create a new timeout manager to use for the test. We set the timeout
	// update frequency 2, so that the resend timeout is dynamically set
	// every other message.
	tm := NewTimeOutManager(nil, WithTimeoutUpdateFrequency(2))

	var wg sync.WaitGroup
	for n := 0; n < b.N; n++ {
		wg.Add(1)
		go func(seq uint8) {
			defer wg.Done()

			msg := &PacketData{Seq: seq}

			tm.Sent(msg, false)
			tm.Received(msg)
		}(uint8(n))
	}

	wg.Wait()
}

// TestStressTestTimeoutMgr tests that the timeout manager can handle a large
// number of concurrent Sent & Received calls, to ensure that the functions does
// not cause any deadlocks.
func TestStressTestTimeoutMgr(t *testing.T) {
	t.Parallel()

	tm := NewTimeOutManager(nil, WithTimeoutUpdateFrequency(2))

	var wg sync.WaitGroup
	for n := 0; n < 100000; n++ {
		wg.Add(1)
		go func(seq uint8) {
			defer wg.Done()

			msg := &PacketData{Seq: seq}

			tm.Sent(msg, false)
			tm.Received(msg)
		}(uint8(n))
	}

	wg.Wait()
}

// TestDynamicTimeout ensures that the resend timeout is dynamically set as
// expected in the timeout manager, with the SYN message that's sent with the
// handshake.
func TestSYNDynamicTimeout(t *testing.T) {
	t.Parallel()

	// Create a new timeout manager to use for the test.
	tm := NewTimeOutManager(nil)

	// First, we'll ensure that the resend timeout doesn't change if we
	// don't send and receive messages.
	noResendTimeoutChange(t, tm, time.Second)

	// Next, we'll simulate that a SYN message has been sent and received.
	// This should change the resend timeout given that the new timeout is
	// greater than the minimum allowed timeout.
	initialResendTimeout := tm.GetResendTimeout()

	synMsg := &PacketSYN{N: 20}

	sendAndReceive(t, tm, synMsg, synMsg, false)

	// The resend timeout should now have dynamically changed. Since the
	// sendAndReceive function waits for one second before simulating the
	// response, execution of the function must have more than 1 sec.
	// We are then sure that the resend timeout has been dynamically
	// set to a value greater default 1 second resend timeout.
	resendTimeout := tm.GetResendTimeout()
	require.Greater(t, resendTimeout, initialResendTimeout)

	// Let's also test that the resend timeout is dynamically set to the
	// expected value, and that the resend multiplier works as expected. If
	// we set the resend multiplier to 10, then send and receive a response
	// after 1 second, then the resend timeout should be around 10 seconds.
	tm.resendMultiplier = 10

	sendAndReceive(t, tm, synMsg, synMsg, false)

	// As it takes a short amount of time to simulate the send and receive
	// of the message, we'll accept a set resend timeout within a range of
	// 10-11 seconds as correct.
	resendTimeout = tm.GetResendTimeout()
	require.InDelta(t, time.Second*10, resendTimeout, float64(time.Second))

	// We'll also test that the resend timeout isn't dynamically set if
	// the new timeout is less than the minimum allowed resend timeout.
	tm.resendMultiplier = 1

	sendAndReceiveWithDuration(
		t, tm, minimumResendTimeout/10, synMsg, synMsg, false,
	)

	newTimeout := tm.GetResendTimeout()
	require.Equal(t, minimumResendTimeout, newTimeout)

	// Then we'll test that the resend timeout isn't dynamically set if
	// when simulating a that the SYN message has been resent.
	sendAndReceive(t, tm, synMsg, synMsg, true)

	unchangedResendTimeout := tm.GetResendTimeout()
	require.Equal(t, newTimeout, unchangedResendTimeout)
}

// TestDataPackageDynamicTimeout ensures that the resend timeout is dynamically
// set as expected in the timeout manager, when PacketData messages and their
// corresponding response are exchanged between the counterparties.
func TestDataPackageDynamicTimeout(t *testing.T) {
	t.Parallel()

	// Create a new timeout manager to use for the test. We set the timeout
	// update frequency to a high value so that we're sure that it's not the
	// reason for the first the resend timeout change.
	tm := NewTimeOutManager(nil, WithTimeoutUpdateFrequency(1000))

	// Next, we'll simulate that a data packet has been sent and received.
	// This should change the resend timeout despite the timeout update
	// frequency being set to a high value, as we never set the resend
	// timeout with in the handshake with by a SYN msg + response.
	initialResendTimeout := tm.GetResendTimeout()

	msg := &PacketData{Seq: 20}
	response := &PacketACK{Seq: 20}

	sendAndReceive(t, tm, msg, response, false)

	// The resend timeout should now have dynamically changed.
	resendTimeout := tm.GetResendTimeout()
	require.NotEqual(t, initialResendTimeout, resendTimeout)

	// Now let's test that the timeout update frequency works as expected.
	// If we set it to 2, we should only update the resend timeout on the
	// second data packet send + receive (as the receive counter in the
	// timeout manager was just reset above when setting the resend
	// timeout).
	tm.timeoutUpdateFrequency = 2

	// We set resend multiplier to a high value, to ensure that the resend
	// timeout is guaranteed to be set to a greater value then the previous
	// resend timeout.
	tm.resendMultiplier = 10

	// The first send and receive should not change the resend timeout.
	sendAndReceive(t, tm, msg, response, false)

	unchangedResendTimeout := tm.GetResendTimeout()
	require.Equal(t, resendTimeout, unchangedResendTimeout)

	// The second send and receive should however change the resend timeout.
	sendAndReceive(t, tm, msg, response, false)

	newResendTimeout := tm.GetResendTimeout()
	require.NotEqual(t, resendTimeout, newResendTimeout)

	// Finally let's test that the resend timeout isn't dynamically set when
	// simulating that the data packet has been resent.
	tm.timeoutUpdateFrequency = 1
	tm.resendMultiplier = 100

	sendAndReceive(t, tm, msg, response, true)

	unchangedResendTimeout = tm.GetResendTimeout()
	require.Equal(t, newResendTimeout, unchangedResendTimeout)
}

// TestStaticTimeout ensures that the resend timeout isn't dynamically set if a
// static timeout has been set.
func TestStaticTimeout(t *testing.T) {
	t.Parallel()

	// Create a new timeout manager with a set static resend timeout to use
	// for the test.
	staticTimeout := time.Second * 2
	tm := NewTimeOutManager(nil, WithStaticResendTimeout(staticTimeout))

	synMsg := &PacketSYN{N: 20}

	// Then ensure that the resend timeout isn't dynamically set if we send
	// and receive messages after setting a static timeout.
	sendAndReceive(t, tm, synMsg, synMsg, false)

	resendTimeout := tm.GetResendTimeout()
	require.Equal(t, staticTimeout, resendTimeout)
}

// sendAndReceive simulates that a SYN message has been sent for the passed the
// timeout manager, and then waits for one second before a simulating the SYN
// response. While waiting, the function asserts that the resend timeout hasn't
// changed.
func sendAndReceive(t *testing.T, tm *TimeoutManager, msg Message,
	response Message, resent bool) {

	t.Helper()

	sendAndReceiveWithDuration(t, tm, time.Second, msg, response, resent)
}

// sendAndReceive simulates that a SYN message has been sent for the passed the
// timeout manager, and then waits for specified delay before a simulating the
// SYN response. While waiting, the function asserts that the resend timeout
// hasn't changed.
func sendAndReceiveWithDuration(t *testing.T, tm *TimeoutManager,
	responseDelay time.Duration, msg Message, response Message,
	resent bool) {

	t.Helper()

	tm.Sent(msg, resent)

	noResendTimeoutChange(t, tm, responseDelay)

	tm.Received(response)
}

// noResendTimeoutChange asserts that the resend timeout hasn't changed for the
// passed timeout manager for the specified duration.
func noResendTimeoutChange(t *testing.T, tm *TimeoutManager,
	duration time.Duration) {

	t.Helper()

	resendTimeout := tm.GetResendTimeout()

	err := wait.Invariant(func() bool {
		return resendTimeout == tm.GetResendTimeout()
	}, duration)
	require.NoError(t, err)
}
