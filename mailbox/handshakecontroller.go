package mailbox

import (
	"crypto/sha512"
	"net"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightningnetwork/lnd/keychain"
)

type HandshakeController struct {
	initiator      bool
	minVersion     byte
	version        byte
	localStatic    keychain.SingleKeyECDH
	remoteStatic   *btcec.PublicKey
	authData       []byte
	passphrase     []byte
	onRemoteStatic func(key *btcec.PublicKey)
	getConn        func(s keychain.SingleKeyECDH, rs *btcec.PublicKey,
		password []byte) (net.Conn, error)
	closeConn func(conn net.Conn) error

	mu sync.Mutex
}

func NewHandshakeController(initiator bool, localStatic keychain.SingleKeyECDH,
	remoteStatic *btcec.PublicKey, authData, password []byte,
	onRemote func(key *btcec.PublicKey),
	getConn func(s keychain.SingleKeyECDH, rs *btcec.PublicKey,
		password []byte) (net.Conn, error),
	closeConn func(conn net.Conn) error,
	opts ...func(hc *HandshakeController)) *HandshakeController {

	hc := &HandshakeController{
		initiator:      initiator,
		minVersion:     MinHandshakeVersion,
		version:        MaxHandshakeVersion,
		localStatic:    localStatic,
		remoteStatic:   remoteStatic,
		authData:       authData,
		passphrase:     password,
		onRemoteStatic: onRemote,
		getConn:        getConn,
		closeConn:      closeConn,
	}

	for _, o := range opts {
		o(hc)
	}

	return hc
}

func WithMinVersion(version byte) func(hc *HandshakeController) {
	return func(hc *HandshakeController) {
		hc.minVersion = version
	}
}

func WithMaxVersion(version byte) func(hc *HandshakeController) {
	return func(hc *HandshakeController) {
		hc.version = version
	}
}

func (h *HandshakeController) doHandshake() (*Machine, net.Conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// If we do not yet have the remote static key or if our expected
	// handshake version is below 2, then we will perform the XX handshake
	// pattern.
	var (
		noise *Machine
		conn1 net.Conn
		err   error
	)
	if h.remoteStatic == nil || h.version < HandshakeVersion2 {
		noise, err = NewBrontideMachine(&BrontideMachineConfig{
			Initiator:           h.initiator,
			HandshakePattern:    XXPattern,
			MinHandshakeVersion: h.minVersion,
			MaxHandshakeVersion: h.version,
			LocalStaticKey:      h.localStatic,
			PAKEPassphrase:      h.passphrase,
			AuthData:            h.authData,
		})
		if err != nil {
			return nil, nil, err
		}

		conn1, err = h.getConn(
			h.localStatic, h.remoteStatic, h.passphrase,
		)
		if err != nil {
			return nil, nil, err
		}

		if err := performHandshake(conn1, noise); err != nil {
			return nil, nil, err
		}

		// At this point, we can now extract the negotiated version and
		// update our version accordingly.
		h.version = noise.version

		// The initiator can now also extract the auth data received
		// from the responder.
		if h.initiator {
			h.authData = noise.receivedPayload
		}

		// If the negotiated version is below 2, the handshake is
		// complete at this point.
		if h.version < HandshakeVersion2 {
			return noise, conn1, nil
		}

		// Otherwise, we have just completed the first handshake in the
		// two-step handshake. At this point we can now extract the
		// remote static key from the noise machine.
		h.remoteStatic = noise.remoteStatic
	}

	// At this point, both sides have agreed to a handshake version more
	// than or equal to 2. So we ensure that our minimum accepted version
	// reflects this.
	if h.minVersion < HandshakeVersion2 {
		h.minVersion = HandshakeVersion2
	}

	// The remote static key is available to us, and so we now perform the
	// KK handshake.
	noise, err = NewBrontideMachine(&BrontideMachineConfig{
		Initiator:           h.initiator,
		HandshakePattern:    KKPattern,
		MinHandshakeVersion: h.minVersion,
		MaxHandshakeVersion: h.version,
		LocalStaticKey:      h.localStatic,
		RemoteStaticKey:     h.remoteStatic,
		AuthData:            h.authData,
	})
	if err != nil {
		return nil, nil, err
	}

	conn, err := h.getConn(h.localStatic, h.remoteStatic, h.passphrase)
	if err != nil {
		return nil, nil, err
	}

	if err := performHandshake(conn, noise); err != nil {
		return nil, nil, err
	}

	// Once the KK handshake has completed successfully, we call the
	// callback function that will be used to persist the remote static key
	// so that any future handshake will jump directly to the KK step.
	h.onRemoteStatic(h.remoteStatic)

	// Once we have persisted the remote static key, we can close conn1 if
	// this handshake switched from one conn to another.
	if conn1 != nil && h.closeConn != nil {
		err := h.closeConn(conn1)
		if err != nil {
			log.Errorf("error closing conn1: %v", err)
		}
	}

	if h.initiator {
		h.authData = noise.receivedPayload
	}

	return noise, conn, nil
}

func performHandshake(conn net.Conn, noise *Machine) error {
	// We'll ensure that we get ActOne from the remote peer in a timely
	// manner. If they don't respond within 1s, then we'll kill the
	// connection.
	err := conn.SetReadDeadline(time.Now().Add(handshakeReadTimeout))
	if err != nil {
		return err
	}

	if err := noise.DoHandshake(conn); err != nil {
		return err
	}

	// We'll reset the deadline as it's no longer critical beyond the
	// initial handshake.
	return conn.SetReadDeadline(time.Time{})
}

func deriveSID(password []byte, remoteKey *btcec.PublicKey,
	localKey keychain.SingleKeyECDH) ([64]byte, error) {

	var (
		entropy = password
		err     error
	)
	if remoteKey != nil {
		entropy, err = ecdh(remoteKey, localKey)
		if err != nil {
			return [64]byte{}, err
		}
	}

	return sha512.Sum512(entropy), nil
}
