package gbn

import "time"

type BackoffWaiter struct {
	currentBackoff time.Duration
	minBackoff     time.Duration
	maxBackoff     time.Duration
}

func NewBackoffWaiter(initial, min, max time.Duration) *BackoffWaiter {
	return &BackoffWaiter{
		currentBackoff: initial,
		minBackoff:     min,
		maxBackoff:     max,
	}
}

func (b *BackoffWaiter) Wait() {
	time.Sleep(b.currentBackoff)

	switch {
	case b.currentBackoff < b.minBackoff:
		b.currentBackoff = b.minBackoff

	case b.currentBackoff < b.maxBackoff:
		b.currentBackoff = b.currentBackoff * 2
		if b.currentBackoff > b.maxBackoff {
			b.currentBackoff = b.maxBackoff
		}
	}
}
