package gbn

import "time"

type Option func(conn *config)

// WithMaxSendSize is used to set the maximum payload size in bytes per packet.
// If set and a large payload comes through then it will be split up into
// multiple packets with payloads no larger than the given maximum size.
// A size of zero will disable splitting.
func WithMaxSendSize(size int) Option {
	return func(conn *config) {
		conn.maxChunkSize = size
	}
}

// WithTimeout is used to set the resend timeout. This is the time to wait
// for ACKs before resending the queue.
func WithTimeout(timeout time.Duration) Option {
	return func(conn *config) {
		conn.resendTimeout = timeout
	}
}

// WithHandshakeTimeout is used to set the timeout used during the handshake.
// If the timeout is reached without response from the peer then the handshake
// will be aborted and restarted.
func WithHandshakeTimeout(timeout time.Duration) Option {
	return func(conn *config) {
		conn.handshakeTimeout = timeout
	}
}

// WithKeepalivePing is used to send a ping packet if no packets have been
// received from the other side for the given duration. This helps keep the
// connection alive and also ensures that the connection is closed if the
// other side does not respond to the ping in a timely manner. After the ping
// the connection will be closed if the other side does not respond within
// time duration.
func WithKeepalivePing(ping, pong time.Duration) Option {
	return func(conn *config) {
		conn.pingTime = ping
		conn.pongTime = pong
	}
}

// WithOnFIN is used to set the onFIN callback that will be called once a FIN
// packet has been received and processed.
func WithOnFIN(fn func()) Option {
	return func(conn *config) {
		conn.onFIN = fn
	}
}
