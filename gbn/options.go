package gbn

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

// WithTimeoutOptions is used to set the different timeout options that will be
// used within gbn package.
func WithTimeoutOptions(opts ...TimeoutOptions) Option {
	return func(conn *config) {
		conn.timeoutOptions = opts
	}
}

// WithOnFIN is used to set the onFIN callback that will be called once a FIN
// packet has been received and processed.
func WithOnFIN(fn func()) Option {
	return func(conn *config) {
		conn.onFIN = fn
	}
}
