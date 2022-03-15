package mailbox

import (
	"errors"
	"fmt"
	"net"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightningnetwork/lnd/keychain"
)

// defaultHandshakes is the maximum number of handshakes that can be done in
// parallel.
const defaultHandshakes = 1000

// Listener is an implementation of a net.Conn which executes an authenticated
// key exchange and message encryption protocol dubbed "Machine" after
// initial connection acceptance. See the Machine struct for additional
// details w.r.t the handshake and encryption scheme used within the
// connection.
type Listener struct {
	hsController *HandshakeController

	tcp *net.TCPListener

	handshakeSema chan struct{}
	conns         chan maybeConn
	quit          chan struct{}
}

// A compile-time assertion to ensure that Conn meets the net.Listener interface.
var _ net.Listener = (*Listener)(nil)

// NewListener returns a new net.Listener which enforces the Brontide scheme
// during both initial connection establishment and data transfer.
func NewListener(passphrase []byte, localStatic keychain.SingleKeyECDH,
	remoteStatic *btcec.PublicKey, listenAddr string, authData []byte,
	onRemoteStatic func(key *btcec.PublicKey)) (*Listener, error) {

	addr, err := net.ResolveTCPAddr("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return nil, err
	}

	hc := &HandshakeController{
		initiator:      false,
		minVersion:     MinHandshakeVersion,
		version:        MaxHandshakeVersion,
		localStatic:    localStatic,
		remoteStatic:   remoteStatic,
		onRemoteStatic: onRemoteStatic,
		passphrase:     passphrase,
		authData:       authData,
	}

	brontideListener := &Listener{
		hsController:  hc,
		tcp:           l,
		handshakeSema: make(chan struct{}, defaultHandshakes),
		conns:         make(chan maybeConn),
		quit:          make(chan struct{}),
	}

	for i := 0; i < defaultHandshakes; i++ {
		brontideListener.handshakeSema <- struct{}{}
	}

	go brontideListener.listen()

	return brontideListener, nil
}

// listen accepts connection from the underlying tcp conn, then performs
// the brontinde handshake procedure asynchronously. A maximum of
// defaultHandshakes will be active at any given time.
//
// NOTE: This method must be run as a goroutine.
func (l *Listener) listen() {
	for {
		select {
		case <-l.handshakeSema:
		case <-l.quit:
			return
		}

		conn, err := l.tcp.Accept()
		if err != nil {
			l.rejectConn(err)
			l.handshakeSema <- struct{}{}
			continue
		}

		go l.doHandshake(conn)
	}
}

// rejectedConnErr is a helper function that prepends the remote address of the
// failed connection attempt to the original error message.
func rejectedConnErr(err error, remoteAddr string) error {
	return fmt.Errorf("unable to accept connection from %v: %v", remoteAddr,
		err)
}

// doHandshake asynchronously performs the brontide handshake, so that it does
// not block the main accept loop. This prevents peers that delay writing to the
// connection from block other connection attempts.
func (l *Listener) doHandshake(conn net.Conn) {
	defer func() { l.handshakeSema <- struct{}{} }()

	remoteAddr := conn.RemoteAddr().String()

	select {
	case <-l.quit:
		return
	default:
	}

	l.hsController.getConn = func(_ keychain.SingleKeyECDH,
		_ *btcec.PublicKey, _ []byte) (net.Conn, error) {

		return conn, nil
	}

	noise, newConn, err := l.hsController.doHandshake()
	if err != nil {
		conn.Close()
		l.rejectConn(rejectedConnErr(err, remoteAddr))
		return
	}

	select {
	case <-l.quit:
		return
	default:
	}

	l.acceptConn(&NoiseConn{
		conn:  newConn,
		noise: noise,
	})
}

// maybeConn holds either a brontide connection or an error returned from the
// handshake.
type maybeConn struct {
	conn *NoiseConn
	err  error
}

// acceptConn returns a connection that successfully performed a handshake.
func (l *Listener) acceptConn(conn *NoiseConn) {
	select {
	case l.conns <- maybeConn{conn: conn}:
	case <-l.quit:
	}
}

// rejectConn returns any errors encountered during connection or handshake.
func (l *Listener) rejectConn(err error) {
	select {
	case l.conns <- maybeConn{err: err}:
	case <-l.quit:
	}
}

// Accept waits for and returns the next connection to the listener. All
// incoming connections are authenticated via the three act Brontide
// key-exchange scheme. This function will fail with a non-nil error in the
// case that either the handshake breaks down, or the remote peer doesn't know
// our static public key.
//
// Part of the net.Listener interface.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case result := <-l.conns:
		return result.conn, result.err
	case <-l.quit:
		return nil, errors.New("brontide connection closed")
	}
}

// Close closes the listener.  Any blocked Accept operations will be unblocked
// and return errors.
//
// Part of the net.Listener interface.
func (l *Listener) Close() error {
	select {
	case <-l.quit:
	default:
		close(l.quit)
	}

	return l.tcp.Close()
}

// Addr returns the listener's network address.
//
// Part of the net.Listener interface.
func (l *Listener) Addr() net.Addr {
	return l.tcp.Addr()
}
