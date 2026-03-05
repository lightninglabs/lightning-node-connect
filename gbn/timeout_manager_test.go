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
	// when simulating a that the SYN message has been resent, but that the
	// handshake timeout is boosted.
	tm.handshakeBooster.boostPercent = 0.2
	originalHandshakeTimeout := tm.GetHandshakeTimeout()

	sendAndReceive(t, tm, synMsg, synMsg, true)

	unchangedResendTimeout := tm.GetResendTimeout()
	require.Equal(t, newTimeout, unchangedResendTimeout)

	newHandshakeTimeout := tm.GetHandshakeTimeout()
	require.Equal(
		t,
		time.Duration(float32(originalHandshakeTimeout)*1.2),
		newHandshakeTimeout,
	)
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
	// simulating that the data packet has been resent. The resend timeout
	// shouldn't be boosted either, as the resend timeout is only boosted
	// if we resend a packet after the duration of the previous resend time.
	tm.timeoutUpdateFrequency = 1
	tm.resendMultiplier = 100

	sendAndReceive(t, tm, msg, response, true)

	unchangedResendTimeout = tm.GetResendTimeout()
	require.Equal(t, newResendTimeout, unchangedResendTimeout)
}

// TestResendBooster tests that the resend timeout booster works as expected,
// and that timeout manager's resendTimeout get's boosted when we need to resend
// a packet again due to not receiving a response within the resend timeout.
func TestResendBooster(t *testing.T) {
	t.Parallel()

	tm := NewTimeOutManager(nil)
	setResendTimeout := time.Millisecond * 1000
	tm.resendTimeout = setResendTimeout

	initialResendTimeout := tm.GetResendTimeout()
	msg := &PacketData{Seq: 20}
	response := &PacketACK{Seq: 20}

	// As the resend timeout won't be dynamically set when we are resending
	// packets, we'll first test that the resend timeout didn't get
	// dynamically updated by a resent data packet. This will however
	// boost the resend timeout, so let's initially set the boost percent
	// to 0 so we can test that the resend timeout wasn't set.
	tm.timeoutUpdateFrequency = 1
	tm.resendMultiplier = 1

	tm.resendBooster.boostPercent = 0

	sendAndReceiveWithDuration(
		t, tm, time.Millisecond, msg, response, true,
	)

	unchangedResendTimeout := tm.GetResendTimeout()
	require.Equal(t, initialResendTimeout, unchangedResendTimeout)

	// Now let's change the boost percent to a non-zero value and test that
	// the resend timeout was boosted as expected.
	tm.resendBooster.boostPercent = 0.1

	changedResendTimeout := tm.GetResendTimeout()

	require.Equal(
		t,
		time.Duration(float32(initialResendTimeout)*1.1),
		changedResendTimeout,
	)

	// Now let's resend another packet again, which shouldn't boost the
	// resend timeout again, as the duration of the previous resend timeout
	// hasn't passed.
	sendAndReceiveWithDuration(
		t, tm, time.Millisecond, msg, response, true,
	)

	unchangedResendTimeout = tm.GetResendTimeout()

	require.Equal(
		t,
		time.Duration(float32(initialResendTimeout)*1.1),
		unchangedResendTimeout,
	)

	// Now let's wait for the duration of the previous resend timeout and
	// then resend another packet. This should boost the resend timeout
	// once more, as the duration of the previous resend timeout has passed.
	err := wait.Invariant(func() bool {
		currentResendTimeout := tm.GetResendTimeout()

		return unchangedResendTimeout == currentResendTimeout
	}, setResendTimeout)
	require.NoError(t, err)

	sendAndReceiveWithDuration(
		t, tm, time.Millisecond, msg, response, true,
	)

	changedResendTimeout = tm.GetResendTimeout()

	require.Equal(
		t,
		time.Duration(float32(initialResendTimeout)*1.2),
		changedResendTimeout,
	)

	// Now let's verify that in case the resend timeout is dynamically set,
	// the boost of the resend timeout is reset. Note that we're not
	// simulating a resend here, as that will dynamically set the resend
	// timeout as the timeout update frequency is set to 1.
	sendAndReceiveWithDuration(
		t, tm, time.Second, msg, response, false,
	)

	newResendTimeout := tm.GetResendTimeout()

	require.NotEqual(t, changedResendTimeout, newResendTimeout)
	require.Equal(t, 0, tm.resendBooster.boostCount)

	// Finally let's check that the resend timeout isn't boosted if we
	// simulate a resend before the duration of the newly set resend
	// timeout hasn't passed.
	sendAndReceiveWithDuration(
		t, tm, time.Millisecond, msg, response, true,
	)

	require.Equal(t, 0, tm.resendBooster.boostCount)

	// But if we wait for the duration of the newly set resend timeout and
	// then simulate a resend, then the resend timeout should be boosted.
	err = wait.Invariant(func() bool {
		currentResendTimeout := tm.GetResendTimeout()

		return newResendTimeout == currentResendTimeout
	}, newResendTimeout)
	require.NoError(t, err)

	sendAndReceiveWithDuration(
		t, tm, time.Millisecond, msg, response, true,
	)

	require.Equal(t, 1, tm.resendBooster.boostCount)
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

// TestDynamicPongTimeout ensures that the pong timeout is dynamically adjusted
// based on the EWMA-smoothed RTT when dynamic pong timeout is enabled.
func TestDynamicPongTimeout(t *testing.T) {
	t.Parallel()

	basePong := 500 * time.Millisecond
	maxPong := 10 * time.Second
	pongMultiplier := 3

	// Use a large ping time so it doesn't cap the pong values under test.
	pingTime := 30 * time.Second

	// Create a timeout manager with dynamic pong timeout enabled.
	tm := NewTimeOutManager(
		nil,
		WithKeepalivePing(pingTime, basePong),
		WithDynamicPongTimeout(pongMultiplier, maxPong),
	)

	// Initially, with no RTT data, the pong time should equal the base.
	require.Equal(t, basePong, tm.GetPongTime())

	// Simulate a SYN exchange with a 200ms RTT. This is the first sample
	// so the EWMA seeds directly: smoothedRTT = 200ms.
	// Dynamic pong = max(basePong, 3 * 200ms) = 600ms.
	synMsg := &PacketSYN{N: 20}
	sendAndReceiveWithDuration(
		t, tm, 200*time.Millisecond, synMsg, synMsg, false,
	)

	pongTime := tm.GetPongTime()
	expectedPong := time.Duration(pongMultiplier) * 200 * time.Millisecond

	// Allow some tolerance for timing jitter.
	require.InDelta(
		t, float64(expectedPong), float64(pongTime),
		float64(100*time.Millisecond),
	)

	// Verify the pong time is above the base.
	require.GreaterOrEqual(t, pongTime, basePong)

	// Now simulate a very fast RTT (50ms). The EWMA blends:
	// smoothedRTT = 0.25*50 + 0.75*200 = 162.5ms.
	// Dynamic pong = 3 * 162.5ms = 487.5ms ~ basePong. With timing
	// jitter the smoothed RTT may be slightly above the theoretical
	// value, so use a tolerance check.
	sendAndReceiveWithDuration(
		t, tm, 50*time.Millisecond, synMsg, synMsg, false,
	)

	pongTime = tm.GetPongTime()
	require.InDelta(
		t, float64(basePong), float64(pongTime),
		float64(100*time.Millisecond),
	)

	// Directly inject a high smoothed RTT to verify the max cap without
	// sleeping through many iterations. 5s smoothedRTT * 3 = 15s which
	// exceeds maxPong (10s), so pong should be capped at maxPong.
	tm.mu.Lock()
	tm.smoothedRTT = 5 * time.Second
	tm.mu.Unlock()

	pongTime = tm.GetPongTime()
	require.Equal(t, maxPong, pongTime)
}

// TestDynamicPongTimeoutDisabled ensures that the pong timeout is static when
// dynamic pong timeout is not enabled.
func TestDynamicPongTimeoutDisabled(t *testing.T) {
	t.Parallel()

	basePong := 3 * time.Second

	// Create a timeout manager without dynamic pong timeout.
	tm := NewTimeOutManager(
		nil,
		WithKeepalivePing(time.Second, basePong),
	)

	// The pong time should always be the base, regardless of RTT.
	require.Equal(t, basePong, tm.GetPongTime())

	// Simulate a SYN exchange with a high RTT.
	synMsg := &PacketSYN{N: 20}
	sendAndReceiveWithDuration(
		t, tm, 2*time.Second, synMsg, synMsg, false,
	)

	// Pong time should still be static.
	require.Equal(t, basePong, tm.GetPongTime())
}

// TestDefaultPongMultiplierAndMaxPongTime verifies that a TimeoutManager
// created without WithDynamicPongTimeout still has the default pongMultiplier
// and maxPongTime values set. This is a regression test for the constructor
// initialization fix: if dynamic mode were later enabled on such a manager
// (e.g. by a new code path), the defaults must produce sensible pong timeouts
// rather than zero-value degradation.
func TestDefaultPongMultiplierAndMaxPongTime(t *testing.T) {
	t.Parallel()

	basePong := 500 * time.Millisecond

	// Create without WithDynamicPongTimeout — the constructor should
	// still initialize pongMultiplier and maxPongTime to defaults.
	tm := NewTimeOutManager(
		nil,
		WithKeepalivePing(30*time.Second, basePong),
	)

	// Manually enable dynamic mode to test the defaults take effect.
	tm.mu.Lock()
	tm.dynamicPongTime = true
	tm.smoothedRTT = 2 * time.Second
	tm.rttInitialized = true
	tm.mu.Unlock()

	pongTime := tm.GetPongTime()

	// With defaults (multiplier=3, max=15s): 3 * 2s = 6s.
	expectedPong := time.Duration(defaultPongMultiplier) * 2 * time.Second
	require.Equal(t, expectedPong, pongTime,
		"default pongMultiplier should produce correct dynamic pong")

	// With a very high RTT, should be capped at defaultMaxPongTime.
	tm.mu.Lock()
	tm.smoothedRTT = 10 * time.Second
	tm.mu.Unlock()

	pongTime = tm.GetPongTime()
	require.Equal(t, defaultMaxPongTime, pongTime,
		"default maxPongTime should cap the dynamic pong")
}

// TestDynamicPongTimeoutWithDataPackets ensures the dynamic pong timeout
// updates correctly when RTT is measured from data packet ACKs.
func TestDynamicPongTimeoutWithDataPackets(t *testing.T) {
	t.Parallel()

	basePong := 500 * time.Millisecond
	pongMultiplier := 3

	tm := NewTimeOutManager(
		nil,
		WithKeepalivePing(time.Second, basePong),
		WithDynamicPongTimeout(pongMultiplier, 15*time.Second),
		WithTimeoutUpdateFrequency(1),
	)

	// Send a data packet and receive the ACK with ~300ms RTT.
	msg := &PacketData{Seq: 1}
	response := &PacketACK{Seq: 1}

	sendAndReceiveWithDuration(
		t, tm, 300*time.Millisecond, msg, response, false,
	)

	// Dynamic pong should be ~900ms (3 * 300ms).
	pongTime := tm.GetPongTime()
	expectedPong := time.Duration(pongMultiplier) * 300 * time.Millisecond

	require.InDelta(
		t, float64(expectedPong), float64(pongTime),
		float64(100*time.Millisecond),
	)
}

// TestDynamicPongCappedByPingTime verifies that the dynamic pong timeout never
// exceeds the ping interval, even when the RTT-based computation would produce
// a larger value.
func TestDynamicPongCappedByPingTime(t *testing.T) {
	t.Parallel()

	basePong := 500 * time.Millisecond
	pingTime := 2 * time.Second
	maxPong := 30 * time.Second
	pongMultiplier := 3

	tm := NewTimeOutManager(
		nil,
		WithKeepalivePing(pingTime, basePong),
		WithDynamicPongTimeout(pongMultiplier, maxPong),
	)

	// Inject a high smoothed RTT: 3 * 1s = 3s > pingTime (2s).
	tm.mu.Lock()
	tm.smoothedRTT = time.Second
	tm.rttInitialized = true
	tm.mu.Unlock()

	pongTime := tm.GetPongTime()
	require.Equal(t, pingTime, pongTime,
		"pong should be capped at pingTime")

	// Even with an extremely high RTT, pong must not exceed pingTime.
	tm.mu.Lock()
	tm.smoothedRTT = 10 * time.Second
	tm.mu.Unlock()

	pongTime = tm.GetPongTime()
	require.Equal(t, pingTime, pongTime,
		"pong must never exceed pingTime regardless of RTT")

	// When the RTT-based value is below pingTime, it should be used.
	tm.mu.Lock()
	tm.smoothedRTT = 200 * time.Millisecond
	tm.mu.Unlock()

	pongTime = tm.GetPongTime()
	expectedPong := time.Duration(pongMultiplier) * 200 * time.Millisecond
	require.Equal(t, expectedPong, pongTime,
		"pong should use RTT-based value when below pingTime")
}

// TestEWMASmoothing verifies that the EWMA-smoothed RTT converges correctly
// and is resistant to single-sample outliers.
func TestEWMASmoothing(t *testing.T) {
	t.Parallel()

	tm := NewTimeOutManager(
		nil,
		WithTimeoutUpdateFrequency(1),
		WithKeepalivePing(30*time.Second, 100*time.Millisecond),
		WithDynamicPongTimeout(3, 30*time.Second),
	)

	// The first sample seeds the EWMA directly.
	synMsg := &PacketSYN{N: 20}
	sendAndReceiveWithDuration(
		t, tm, 200*time.Millisecond, synMsg, synMsg, false,
	)

	rtt := tm.GetSmoothedRTT()
	require.InDelta(
		t, float64(200*time.Millisecond), float64(rtt),
		float64(50*time.Millisecond),
		"first sample should seed EWMA directly",
	)

	// Feed 10 stable samples at 200ms. The EWMA should stay near 200ms.
	for i := 0; i < 10; i++ {
		sendAndReceiveWithDuration(
			t, tm, 200*time.Millisecond, synMsg, synMsg, false,
		)
	}

	stableRTT := tm.GetSmoothedRTT()
	require.InDelta(
		t, float64(200*time.Millisecond), float64(stableRTT),
		float64(50*time.Millisecond),
		"EWMA should converge near stable RTT",
	)

	// Now inject a single outlier (50ms). The EWMA should NOT drop
	// dramatically — it should resist the outlier due to smoothing.
	sendAndReceiveWithDuration(
		t, tm, 50*time.Millisecond, synMsg, synMsg, false,
	)

	afterOutlier := tm.GetSmoothedRTT()

	// EWMA with alpha=0.25: new = 0.25*50 + 0.75*~200 = ~162ms.
	// It should still be well above the outlier value.
	require.Greater(t, int64(afterOutlier), int64(100*time.Millisecond),
		"EWMA should resist single low outlier")
	require.Less(t, int64(afterOutlier), int64(stableRTT),
		"EWMA should move slightly toward outlier")

	// Inject a single high outlier (2s). Should move up but not jump to 2s.
	sendAndReceiveWithDuration(
		t, tm, 2*time.Second, synMsg, synMsg, false,
	)

	afterHighOutlier := tm.GetSmoothedRTT()
	require.Less(t, int64(afterHighOutlier), int64(time.Second),
		"EWMA should resist single high outlier")
	require.Greater(t, int64(afterHighOutlier), int64(afterOutlier),
		"EWMA should move toward high outlier")
}

// TestGetLatestRTT ensures GetLatestRTT returns the most recently measured RTT.
func TestGetLatestRTT(t *testing.T) {
	t.Parallel()

	tm := NewTimeOutManager(nil, WithTimeoutUpdateFrequency(1))

	// Initially zero.
	require.Equal(t, time.Duration(0), tm.GetLatestRTT())

	// After a SYN exchange, should reflect the response time.
	synMsg := &PacketSYN{N: 20}
	sendAndReceiveWithDuration(
		t, tm, time.Second, synMsg, synMsg, false,
	)

	rtt := tm.GetLatestRTT()
	require.InDelta(
		t, float64(time.Second), float64(rtt),
		float64(100*time.Millisecond),
	)
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
