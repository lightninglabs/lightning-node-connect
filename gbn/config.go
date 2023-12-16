package gbn

import "time"

// TimeoutOptions can be used to modify the default timeout values used within
// the TimeoutManager.
type TimeoutOptions func(manager *TimeoutManager)

// WithStaticResendTimeout is used to set a static resend timeout. This is the
// time to wait for ACKs before resending the queue.
func WithStaticResendTimeout(timeout time.Duration) TimeoutOptions {
	return func(manager *TimeoutManager) {
		manager.useStaticTimeout = true
		manager.resendTimeout = timeout
	}
}

// WithResendMultiplier is used to set the resend multiplier. This is the
// multiplier we use when dynamically setting the resend timeout, based on how
// long it took for other party to respond.
// Note that when setting the resend timeout manually with the
// WithStaticResendTimeout option, this option will have no effect.
// Note that the passed multiplier must be greater than zero or this option will
// have no effect.
func WithResendMultiplier(multiplier int) TimeoutOptions {
	return func(manager *TimeoutManager) {
		if multiplier > 0 {
			manager.resendMultiplier = multiplier
		}
	}
}

// WithTimeoutUpdateFrequency is used to set the frequency of how many
// corresponding responses we need to receive until updating the resend timeout.
// Note that when setting the resend timeout manually with the WithTimeout
// option, this option will have no effect.
// Also note that the passed frequency must be greater than zero or this option
// will have no effect.
func WithTimeoutUpdateFrequency(frequency int) TimeoutOptions {
	return func(manager *TimeoutManager) {
		if frequency > 0 {
			manager.timeoutUpdateFrequency = frequency
		}
	}
}

// WithHandshakeTimeout is used to set the timeout used during the handshake.
// If the timeout is reached without response from the peer then the handshake
// will be aborted and restarted.
func WithHandshakeTimeout(timeout time.Duration) TimeoutOptions {
	return func(manager *TimeoutManager) {
		manager.handshakeTimeout = timeout
	}
}

// WithKeepalivePing is used to send a ping packet if no packets have been
// received from the other side for the given duration. This helps keep the
// connection alive and also ensures that the connection is closed if the
// other side does not respond to the ping in a timely manner. After the ping
// the connection will be closed if the other side does not respond within
// time duration.
func WithKeepalivePing(ping, pong time.Duration) TimeoutOptions {
	return func(manager *TimeoutManager) {
		manager.pingTime = ping
		manager.pongTime = pong
	}
}

// config holds the configuration values for an instance of GoBackNConn.
type config struct {
	// n is the window size. The sender can send a maximum of n packets
	// before requiring an ack from the receiver for the first packet in
	// the window. The value of n is chosen by the client during the
	// GoBN handshake.
	n uint8

	// s is the maximum sequence number used to label packets. Packets
	// are labelled with incrementing sequence numbers modulo s.
	// s must be strictly larger than the window size, n. This
	// is so that the receiver can tell if the sender is resending the
	// previous window (maybe the sender did not receive the acks) or if
	// they are sending the next window. If s <= n then there would be
	// no way to tell.
	s uint8

	// maxChunkSize is the maximum payload size in bytes allowed per
	// message. If the payload to be sent is larger than maxChunkSize then
	// the payload will be split between multiple packets.
	// If maxChunkSize is zero then it is disabled and data won't be split
	// between packets.
	maxChunkSize int

	// recvFromStream is the function that will be used to acquire the next
	// available packet.
	recvFromStream recvBytesFunc

	// sendToStream is the function that will be used to send over our next
	// packet.
	sendToStream sendBytesFunc

	// onFIN is a callback that if set, will be called once a FIN packet has
	// been received and processed.
	onFIN func()

	timeoutOptions []TimeoutOptions
}

// newConfig constructs a new config struct.
func newConfig(sendFunc sendBytesFunc, recvFunc recvBytesFunc,
	n uint8) *config {

	return &config{
		n:              n,
		s:              n + 1,
		recvFromStream: recvFunc,
		sendToStream:   sendFunc,
	}
}
