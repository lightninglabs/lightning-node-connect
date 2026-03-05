package gbn

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// drawDuration draws a random time.Duration in the given range using rapid.
func drawDuration(t *rapid.T, min, max time.Duration,
	label string) time.Duration {

	return time.Duration(
		rapid.Int64Range(int64(min), int64(max)).Draw(t, label),
	)
}

// TestRapidMessageRoundTrip checks that for any valid GBN message, the
// serialize-then-deserialize round trip is lossless.
func TestRapidMessageRoundTrip(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		msg := genMessage(t)

		serialized, err := msg.Serialize()
		require.NoError(t, err)

		deserialized, err := Deserialize(serialized)
		require.NoError(t, err)

		require.Equal(t, msg, deserialized)
	})
}

// TestRapidDeserializeNeverPanics checks that Deserialize never panics on
// arbitrary input bytes, only returning errors or valid messages.
func TestRapidDeserializeNeverPanics(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		data := rapid.SliceOf(rapid.Byte()).Draw(t, "data")

		// This must not panic regardless of input.
		msg, err := Deserialize(data)
		if err != nil {
			return
		}

		// If deserialization succeeded, the message should be
		// re-serializable.
		_, err = msg.Serialize()
		require.NoError(t, err)
	})
}

// TestRapidContainsSequenceProperties verifies key invariants of the modular
// arithmetic sequence containment check used by the GBN queue.
func TestRapidContainsSequenceProperties(t *testing.T) {
	t.Parallel()

	// Property: base is never contained when base == top (empty queue).
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.Uint8().Draw(t, "base")
		require.False(t, containsSequence(base, base, base))
	})

	// Property: base is always contained when queue is non-empty.
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.Uint8().Draw(t, "base")
		top := rapid.Uint8().Draw(t, "top")
		if base == top {
			t.Skip()
		}
		require.True(t, containsSequence(base, top, base))
	})

	// Property: top is never contained (half-open interval [base, top)).
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.Uint8().Draw(t, "base")
		top := rapid.Uint8().Draw(t, "top")
		if base == top {
			t.Skip()
		}
		require.False(t, containsSequence(base, top, top))
	})

	// Property: (top-1) mod 256 is always contained when non-empty.
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.Uint8().Draw(t, "base")
		top := rapid.Uint8().Draw(t, "top")
		if base == top {
			t.Skip()
		}
		lastValid := top - 1 // uint8 wraps naturally
		require.True(t, containsSequence(base, top, lastValid))
	})
}

// TestRapidContainsSequenceBruteForce exhaustively validates containsSequence
// against a simple reference implementation for all s values in range.
func TestRapidContainsSequenceBruteForce(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		// Use a small modular space to make brute force feasible.
		s := rapid.Uint8Range(2, 20).Draw(t, "s")
		base := rapid.Uint8Range(0, s-1).Draw(t, "base")
		top := rapid.Uint8Range(0, s-1).Draw(t, "top")
		seq := rapid.Uint8Range(0, s-1).Draw(t, "seq")

		expected := refContains(base, top, seq, s)
		got := containsSequence(base, top, seq)
		require.Equal(t, expected, got,
			"base=%d top=%d seq=%d s=%d", base, top, seq, s)
	})
}

// refContains is a reference implementation of sequence containment that
// enumerates the half-open interval [base, top) modulo s.
func refContains(base, top, seq, s uint8) bool {
	if base == top {
		return false
	}
	cur := base
	for {
		if cur == seq {
			return true
		}
		cur = (cur + 1) % s
		if cur == top {
			return false
		}
	}
}

// TestRapidDynamicPongTimeoutProperties verifies the key invariants of the
// dynamic pong timeout computation.
func TestRapidDynamicPongTimeoutProperties(t *testing.T) {
	t.Parallel()

	// Property: pong timeout is always >= basePongTime when pingTime >=
	// basePong (i.e., the pingTime cap doesn't interfere).
	rapid.Check(t, func(t *rapid.T) {
		basePong := drawDuration(
			t, time.Millisecond, 10*time.Second, "basePong",
		)
		pingTime := drawDuration(
			t, basePong, 60*time.Second, "pingTime",
		)
		multiplier := rapid.IntRange(1, 10).Draw(t, "mult")
		maxPong := drawDuration(
			t, basePong, 60*time.Second, "maxPong",
		)
		rtt := drawDuration(
			t, time.Millisecond, 20*time.Second, "rtt",
		)

		tm := NewTimeOutManager(
			nil,
			WithKeepalivePing(pingTime, basePong),
			WithDynamicPongTimeout(multiplier, maxPong),
		)

		// Inject RTT.
		tm.mu.Lock()
		tm.smoothedRTT = rtt
		tm.rttInitialized = true
		tm.mu.Unlock()

		pong := tm.GetPongTime()

		require.GreaterOrEqual(t, int64(pong), int64(basePong),
			"pong=%v must be >= basePong=%v (rtt=%v, ping=%v)",
			pong, basePong, rtt, pingTime)
	})

	// Property: pong timeout is always <= maxPongTime when max is set.
	rapid.Check(t, func(t *rapid.T) {
		basePong := drawDuration(
			t, time.Millisecond, 5*time.Second, "basePong",
		)
		multiplier := rapid.IntRange(1, 10).Draw(t, "mult")
		maxPong := drawDuration(
			t, basePong, 60*time.Second, "maxPong",
		)
		rtt := drawDuration(
			t, time.Millisecond, 20*time.Second, "rtt",
		)

		tm := NewTimeOutManager(
			nil,
			WithKeepalivePing(10*time.Second, basePong),
			WithDynamicPongTimeout(multiplier, maxPong),
		)

		tm.mu.Lock()
		tm.smoothedRTT = rtt
		tm.rttInitialized = true
		tm.mu.Unlock()

		pong := tm.GetPongTime()

		require.LessOrEqual(t, int64(pong), int64(maxPong),
			"pong=%v must be <= maxPong=%v (rtt=%v, mult=%d)",
			pong, maxPong, rtt, multiplier)
	})

	// Property: pong timeout never exceeds ping interval.
	rapid.Check(t, func(t *rapid.T) {
		basePong := drawDuration(
			t, time.Millisecond, 5*time.Second, "basePong",
		)
		pingTime := drawDuration(
			t, time.Millisecond, 30*time.Second, "pingTime",
		)
		multiplier := rapid.IntRange(1, 10).Draw(t, "mult")
		maxPong := drawDuration(
			t, basePong, 60*time.Second, "maxPong",
		)
		rtt := drawDuration(
			t, time.Millisecond, 20*time.Second, "rtt",
		)

		tm := NewTimeOutManager(
			nil,
			WithKeepalivePing(pingTime, basePong),
			WithDynamicPongTimeout(multiplier, maxPong),
		)

		tm.mu.Lock()
		tm.smoothedRTT = rtt
		tm.rttInitialized = true
		tm.mu.Unlock()

		pong := tm.GetPongTime()

		require.LessOrEqual(t, int64(pong), int64(pingTime),
			"pong=%v must be <= pingTime=%v (rtt=%v, mult=%d, "+
				"base=%v, max=%v)",
			pong, pingTime, rtt, multiplier, basePong, maxPong)
	})

	// Property: with zero RTT, pong falls back to static base.
	rapid.Check(t, func(t *rapid.T) {
		basePong := drawDuration(
			t, time.Millisecond, 30*time.Second, "basePong",
		)

		tm := NewTimeOutManager(
			nil,
			WithKeepalivePing(10*time.Second, basePong),
			WithDynamicPongTimeout(3, 15*time.Second),
		)

		pong := tm.GetPongTime()
		require.Equal(t, basePong, pong)
	})

	// Property: without dynamic enabled, pong is always the static base.
	rapid.Check(t, func(t *rapid.T) {
		basePong := drawDuration(
			t, time.Millisecond, 30*time.Second, "basePong",
		)
		rtt := drawDuration(
			t, time.Millisecond, 20*time.Second, "rtt",
		)

		tm := NewTimeOutManager(
			nil,
			WithKeepalivePing(10*time.Second, basePong),
		)

		// Even with RTT injected, static mode ignores it.
		tm.mu.Lock()
		tm.smoothedRTT = rtt
		tm.rttInitialized = true
		tm.mu.Unlock()

		pong := tm.GetPongTime()
		require.Equal(t, basePong, pong)
	})
}

// TestRapidEWMAProperties verifies EWMA smoothed RTT invariants under random
// sample sequences.
func TestRapidEWMAProperties(t *testing.T) {
	t.Parallel()

	// Property: smoothedRTT is always bounded by [min(samples), max(samples)]
	// after at least one sample.
	rapid.Check(t, func(t *rapid.T) {
		nSamples := rapid.IntRange(1, 50).Draw(t, "nSamples")

		tm := NewTimeOutManager(nil)

		var minSample, maxSample time.Duration

		tm.mu.Lock()
		for i := 0; i < nSamples; i++ {
			sample := drawDuration(
				t, time.Millisecond, 30*time.Second,
				"sample",
			)

			if i == 0 {
				minSample = sample
				maxSample = sample
			} else {
				if sample < minSample {
					minSample = sample
				}
				if sample > maxSample {
					maxSample = sample
				}
			}

			tm.updateSmoothedRTT(sample)
		}

		smoothed := tm.smoothedRTT
		tm.mu.Unlock()

		require.GreaterOrEqual(t, int64(smoothed), int64(minSample),
			"smoothedRTT=%v must be >= min sample=%v",
			smoothed, minSample)
		require.LessOrEqual(t, int64(smoothed), int64(maxSample),
			"smoothedRTT=%v must be <= max sample=%v",
			smoothed, maxSample)
	})

	// Property: constant samples converge to that constant.
	rapid.Check(t, func(t *rapid.T) {
		constant := drawDuration(
			t, time.Millisecond, 30*time.Second, "constant",
		)

		tm := NewTimeOutManager(nil)

		tm.mu.Lock()
		for i := 0; i < 100; i++ {
			tm.updateSmoothedRTT(constant)
		}
		smoothed := tm.smoothedRTT
		tm.mu.Unlock()

		require.Equal(t, constant, smoothed,
			"100 identical samples should converge exactly")
	})
}

// TestRapidQueueSizeInvariants uses a state machine approach to verify that
// the GBN queue's size never exceeds the window and that sequence numbers
// remain consistent after a series of add/ACK/NACK operations.
func TestRapidQueueSizeInvariants(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		// Use a small window to increase the chance of wrapping.
		n := rapid.Uint8Range(2, 8).Draw(t, "n")
		s := n + 1

		tm := NewTimeOutManager(nil)

		q := newQueue(&queueCfg{
			s:   s,
			log: log,
			sendPkt: func(packet *PacketData) error {
				return nil
			},
		}, tm)
		defer q.stop()

		// Track how many packets we've added and ACK'd to verify
		// the queue size invariant.
		added := 0
		acked := 0

		numOps := rapid.IntRange(10, 100).Draw(t, "numOps")

		for i := 0; i < numOps; i++ {
			currentSize := int(q.size())
			maxSize := int(n)

			if currentSize < maxSize {
				op := rapid.IntRange(0, 2).Draw(t, "op")

				switch op {
				case 0:
					// Add a packet.
					q.addPacket(&PacketData{
						Payload: []byte{byte(i)},
					})
					added++

				case 1:
					// ACK the base if queue is non-empty.
					if currentSize > 0 {
						q.baseMtx.RLock()
						base := q.sequenceBase
						q.baseMtx.RUnlock()

						if q.processACK(base) {
							acked++
						}
					}

				case 2:
					// NACK some sequence.
					if currentSize > 0 {
						q.baseMtx.RLock()
						base := q.sequenceBase
						q.baseMtx.RUnlock()

						q.processNACK(base)
					}
				}
			} else {
				// Queue full, must ACK to make room.
				q.baseMtx.RLock()
				base := q.sequenceBase
				q.baseMtx.RUnlock()

				if q.processACK(base) {
					acked++
				}
			}

			// Invariant: queue size must never exceed the
			// window size n.
			size := q.size()
			require.LessOrEqual(t, size, n,
				"queue size %d exceeds window %d after "+
					"op %d (added=%d, acked=%d)",
				size, n, i, added, acked)

			// Invariant: queue size should equal
			// (added - acked) mod s.
			expectedSize := uint8((added - acked) % int(s))
			require.Equal(t, expectedSize, size,
				"size mismatch: expected %d got %d "+
					"(added=%d acked=%d s=%d)",
				expectedSize, size, added, acked, s)
		}
	})
}

// TestRapidTimeoutBoosterProperties verifies that the timeout booster always
// produces values >= the original timeout, and that reset returns to base.
func TestRapidTimeoutBoosterProperties(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		originalTimeout := drawDuration(
			t, time.Millisecond, 30*time.Second,
			"originalTimeout",
		)

		boostPct := float32(
			rapid.Float64Range(0.01, 2.0).Draw(t, "boostPct"),
		)

		booster := NewTimeoutBooster(originalTimeout, boostPct, false)

		numBoosts := rapid.IntRange(0, 20).Draw(t, "numBoosts")
		for i := 0; i < numBoosts; i++ {
			booster.Boost()
		}

		current := booster.GetCurrentTimeout()

		// Property: boosted timeout is always >= original.
		require.GreaterOrEqual(t, int64(current),
			int64(originalTimeout),
			"boosted timeout %v < original %v after %d boosts",
			current, originalTimeout, numBoosts)

		// Property: after reset, timeout returns to the new base.
		newBase := drawDuration(
			t, time.Millisecond, 30*time.Second, "newBase",
		)

		booster.Reset(newBase)
		require.Equal(t, newBase, booster.GetCurrentTimeout())
	})
}

// TestRapidPingPongZeroDisablesPing verifies that a zero ping time effectively
// disables keepalive by returning MaxInt64.
func TestRapidPingPongZeroDisablesPing(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(t *rapid.T) {
		tm := NewTimeOutManager(nil)

		pingTime := tm.GetPingTime()
		require.Equal(t, time.Duration(math.MaxInt64), pingTime)
	})
}

// genMessage generates a random valid GBN message.
func genMessage(t *rapid.T) Message {
	msgType := rapid.IntRange(0, 5).Draw(t, "msgType")

	switch msgType {
	case 0:
		return &PacketData{
			Seq:        rapid.Uint8().Draw(t, "seq"),
			FinalChunk: rapid.Bool().Draw(t, "finalChunk"),
			IsPing:     rapid.Bool().Draw(t, "isPing"),
			Payload:    rapid.SliceOf(rapid.Byte()).Draw(t, "payload"),
		}
	case 1:
		return &PacketACK{
			Seq: rapid.Uint8().Draw(t, "seq"),
		}
	case 2:
		return &PacketNACK{
			Seq: rapid.Uint8().Draw(t, "seq"),
		}
	case 3:
		return &PacketSYN{
			N: rapid.Uint8().Draw(t, "n"),
		}
	case 4:
		return &PacketFIN{}
	default:
		return &PacketSYNACK{}
	}
}
