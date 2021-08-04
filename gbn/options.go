package gbn

type Option func(conn *GoBackNConn)

func WithMaxSendSize(size int) Option {
	return func(conn *GoBackNConn) {
		conn.maxChunkSize = size
	}
}
