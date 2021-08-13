package gbn

import "time"

type Option func(conn *GoBackNConn)

// WithMaxSendSize is used to set the maximum payload size in bytes per packet.
// If set and a large payload comes through then it will be split up into
// multiple packets with payloads no larger than the given maximum size.
// A size of zero will disable splitting.
func WithMaxSendSize(size int) Option {
	return func(conn *GoBackNConn) {
		conn.maxChunkSize = size
	}
}

// WithTimeout is used to set the resend timeout. This is the time to wait
// for ACKs before resending the queue.
func WithTimeout(timeout time.Duration) Option {
	return func(conn *GoBackNConn) {
		conn.resendTimeout = timeout
	}
}

// WithHandshakeTimeout is used to set the timeout used during the handshake.
// If the timeout is reached without response from the peer then the handshake
// will be aborted and restarted.
func WithHandshakeTimeout(timeout time.Duration) Option {
	return func(conn *GoBackNConn) {
		conn.handshakeTimeout = timeout
	}
}
