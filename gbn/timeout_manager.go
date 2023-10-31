package gbn

import (
	"math"
	"sync"
	"time"

	"github.com/btcsuite/btclog"
)

const (
	defaultHandshakeTimeout       = 1000 * time.Millisecond
	defaultResendTimeout          = 1000 * time.Millisecond
	minimumResendTimeout          = 1000 * time.Millisecond
	defaultFinSendTimeout         = 1000 * time.Millisecond
	defaultResendMultiplier       = 5
	defaultTimeoutUpdateFrequency = 100
	DefaultSendTimeout            = math.MaxInt64
	DefaultRecvTimeout            = math.MaxInt64
)

// TimeoutManager manages the different timeouts used by the gbn package.
type TimeoutManager struct {
	// useStaticTimeout is used to indicate whether the resendTimeout
	// has been manually set, and if so, should not be updated dynamically.
	useStaticTimeout bool

	// hasSetDynamicTimeout is used to indicate whether the resendTimeout
	// has ever been set dynamically.
	hasSetDynamicTimeout bool

	// resendTimeout defines the current resend timeout used by the
	// timeout manager.
	// The resend timeout is the duration that will be waited before
	// resending the packets in the current queue. The timeout is set
	// dynamically, and is set to the time it took for the other party to
	// respond, multiplied by the resendMultiplier.
	resendTimeout time.Duration

	// resendMultiplier defines the multiplier used when multiplying the
	// duration it took for the other party to respond when setting the
	// resendTimeout.
	resendMultiplier int

	// latestSentSYNTime is used to keep track of the time when the latest
	// SYN message was sent. This is used to dynamically set the resend
	// timeout, based on how long it took for the other party to respond to
	// the SYN message.
	latestSentSYNTime time.Time

	// latestSentSYNTimeMu should be locked when updating or accessing the
	// latestSentSYNTime field.
	latestSentSYNTimeMu sync.Mutex

	// handshakeTimeout is the time after which the server or client
	// will abort and restart the handshake if the expected response is
	// not received from the peer.
	handshakeTimeout time.Duration

	// finSendTimeout is the timeout after which the created context for
	// sending a FIN message will be time out.
	finSendTimeout time.Duration

	// sendTimeout defines the max time we will wait to send a msg before
	// timing out.
	sendTimeout time.Duration

	// recvTimeout defines the max time we will wait to receive a msg before
	// timing out.
	recvTimeout time.Duration

	// pingTime represents at which frequency we will send pings to the
	// counterparty if we've received no packet.
	pingTime time.Duration

	// pongTime represents how long we will wait for the expect a pong
	// response after we've sent a ping. If no response is received within
	// the time limit, we will close the connection.
	pongTime time.Duration

	// responseCounter represents the current number of corresponding
	// responses received since last updating the resend timeout.
	responseCounter int

	// timeoutUpdateFrequency represents the frequency of how many
	// corresponding responses we need to receive until the resend timeout
	// will be updated.
	timeoutUpdateFrequency int

	log btclog.Logger

	sentTimes   map[uint8]time.Time
	sentTimesMu sync.Mutex

	// mu should be locked when updating or accessing any of timeout
	// manager's timeout fields. It should also be held when accessing any
	// of the timeout manager's fields that get updated throughout the
	// lifecycle of the timeout manager after initialization, that doesn't
	// have a dedicated mutex.
	//
	// Note that the lock order for this mutex is before any of the other
	// mutexes in the timeout manager.
	mu sync.RWMutex
}

// NewTimeOutManager creates a new timeout manager.
func NewTimeOutManager(logger btclog.Logger,
	timeoutOpts ...TimeoutOptions) *TimeoutManager {

	if logger == nil {
		logger = log
	}

	m := &TimeoutManager{
		log:                    logger,
		resendTimeout:          defaultResendTimeout,
		handshakeTimeout:       defaultHandshakeTimeout,
		useStaticTimeout:       false,
		resendMultiplier:       defaultResendMultiplier,
		finSendTimeout:         defaultFinSendTimeout,
		recvTimeout:            DefaultRecvTimeout,
		sendTimeout:            DefaultSendTimeout,
		sentTimes:              make(map[uint8]time.Time),
		timeoutUpdateFrequency: defaultTimeoutUpdateFrequency,
	}

	for _, opt := range timeoutOpts {
		opt(m)
	}

	return m
}

// Sent should be called when a message is sent by the connection. The resent
// parameter should be set to true if the message is a resent message.
func (m *TimeoutManager) Sent(msg Message, resent bool) {
	if m.useStaticTimeout {
		return
	}

	sentAt := time.Now()

	// We will dynamically update the resend timeout throughout the lifetime
	// of the connection, to ensure that it reflects the current response
	// time. Therefore, we'll keep track of when we sent a package, and when
	// we receive the corresponding response.
	// If we're resending a message, we can't know if a corresponding
	// response is the response to the resent message, or the original
	// message. Therefore, we never update the resend timeout after
	// resending a message.
	switch msg := msg.(type) {
	case *PacketSYN:
		m.latestSentSYNTimeMu.Lock()
		defer m.latestSentSYNTimeMu.Unlock()

		if !resent {
			m.latestSentSYNTime = sentAt

			return
		}

		// If we've resent the SYN, we'll reset the latestSentSYNTime to
		// the zero value, to ensure that we don't update the resend
		// timeout based on the corresponding response, as we can't know
		// if the response is for the resent SYN or the original SYN.
		m.latestSentSYNTime = time.Time{}

	case *PacketData:
		m.sentTimesMu.Lock()
		defer m.sentTimesMu.Unlock()

		if resent {
			// If we're resending a data packet, we'll delete the
			// sent time for the sequence, to ensure that we won't
			// update the resend timeout when we receive the
			// corresponding response.
			delete(m.sentTimes, msg.Seq)

			return
		}

		m.sentTimes[msg.Seq] = sentAt
	}
}

// Received should be called when a message is received by the connection.
func (m *TimeoutManager) Received(msg Message) {
	if m.useStaticTimeout {
		return
	}

	receivedAt := time.Now()

	// We lock the TimeoutManager's mu as soon as Received is executed, to
	// ensure that any GetResendTimeout call we receive concurrently after
	// this Received call, will return an updated resend timeout if this
	// Received call does update the timeout.
	m.mu.Lock()
	defer m.mu.Unlock()

	switch msg := msg.(type) {
	case *PacketSYN, *PacketSYNACK:
		m.latestSentSYNTimeMu.Lock()

		if m.latestSentSYNTime.IsZero() {
			m.latestSentSYNTimeMu.Unlock()

			return
		}

		responseTime := receivedAt.Sub(m.latestSentSYNTime)

		m.latestSentSYNTime = time.Time{}

		m.latestSentSYNTimeMu.Unlock()

		m.updateResendTimeoutUnsafe(responseTime)

	case *PacketACK:
		m.sentTimesMu.Lock()

		sentTime, ok := m.sentTimes[msg.Seq]
		if !ok {
			m.sentTimesMu.Unlock()

			return
		}

		delete(m.sentTimes, msg.Seq)

		m.sentTimesMu.Unlock()

		m.responseCounter++

		reachedFrequency := m.responseCounter%
			m.timeoutUpdateFrequency == 0

		// In case we never set the resend timeout dynamically in the
		// handshake due to needing to resend the SYN, or if we've
		// reached received the number of packages matching the
		// timeoutUpdateFrequency, we'll update the resend timeout.
		if !m.hasSetDynamicTimeout || reachedFrequency {
			m.responseCounter = 0

			m.updateResendTimeoutUnsafe(receivedAt.Sub(sentTime))
		}
	}
}

// updateResendTimeout updates the resend timeout based on the given response
// time. The resend timeout will be only be updated if the given response time
// is greater than the default resend timeout, after being multiplied by the
// resendMultiplier.
//
// NOTE: This function TimeoutManager mu must be held when calling this
// function.
func (m *TimeoutManager) updateResendTimeoutUnsafe(responseTime time.Duration) {
	m.hasSetDynamicTimeout = true

	multipliedTimeout := time.Duration(m.resendMultiplier) * responseTime

	if multipliedTimeout < minimumResendTimeout {
		m.log.Tracef("Setting resendTimeout to minimumResendTimeout "+
			"%v as the new dynamic timeout %v is not greater than "+
			"the minimum resendTimeout.",
			m.resendTimeout, multipliedTimeout)
		multipliedTimeout = minimumResendTimeout
	}

	m.log.Tracef("Updating resendTimeout to %v", multipliedTimeout)

	m.resendTimeout = multipliedTimeout
}

// GetResendTimeout returns the current resend timeout.
func (m *TimeoutManager) GetResendTimeout() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.resendTimeout
}

// GetHandshakeTimeout returns the handshake timeout.
func (m *TimeoutManager) GetHandshakeTimeout() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.handshakeTimeout
}

// GetFinSendTimeout returns the fin send timeout.
func (m *TimeoutManager) GetFinSendTimeout() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.finSendTimeout
}

// GetSendTimeout returns the send timeout.
func (m *TimeoutManager) GetSendTimeout() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.sendTimeout
}

// GetRecvTimeout returns the recv timeout.
func (m *TimeoutManager) GetRecvTimeout() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.recvTimeout
}

// GetPingTime returns the ping time, representing at which frequency we will
// send pings to the counterparty if we've received no packet.
func (m *TimeoutManager) GetPingTime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.pingTime == 0 {
		return time.Duration(math.MaxInt64)
	}

	return m.pingTime
}

// GetPongTime returns the pong timeout, representing how long we will wait for
// the expect a pong response after we've sent a ping. If no response is
// received within the time limit, we will close the connection.
func (m *TimeoutManager) GetPongTime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.pongTime == 0 {
		return time.Duration(math.MaxInt64)
	}

	return m.pongTime
}

// SetSendTimeout sets the send timeout.
func (m *TimeoutManager) SetSendTimeout(timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sendTimeout = timeout
}

// SetRecvTimeout sets the receive timeout.
func (m *TimeoutManager) SetRecvTimeout(timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.recvTimeout = timeout
}
