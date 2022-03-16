package mailbox

import (
	"crypto/sha512"
	"net"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightningnetwork/lnd/keychain"
)

type HandshakeMgr struct {
	cfg *HandshakeMgrConfig

	minVersion byte
	version    byte

	remoteStatic *btcec.PublicKey
	authData     []byte

	mu sync.Mutex
}

type HandshakeMgrConfig struct {
	Initiator    bool
	LocalStatic  keychain.SingleKeyECDH
	RemoteStatic *btcec.PublicKey
	AuthData     []byte
	Passphrase   []byte

	OnRemoteStatic func(key *btcec.PublicKey)

	GetConn func(s keychain.SingleKeyECDH, rs *btcec.PublicKey,
		password []byte) (net.Conn, error)

	CloseConn func(conn net.Conn) error
}

func NewHandshakeMgr(cfg *HandshakeMgrConfig,
	opts ...func(mgr *HandshakeMgr)) *HandshakeMgr {

	mgr := &HandshakeMgr{
		cfg:        cfg,
		minVersion: MinHandshakeVersion,
		version:    MaxHandshakeVersion,
	}

	for _, o := range opts {
		o(mgr)
	}

	return mgr
}

// WithMinVersion is a functional option that can be used to set the min
// handshake version of the HandshakeMgr if it should differ from the default.
func WithMinVersion(version byte) func(hc *HandshakeMgr) {
	return func(hc *HandshakeMgr) {
		hc.minVersion = version
	}
}

// WithMaxVersion is a functional option that can be used to set the max
// handshake version of the HandshakeMgr if it should differ from the default.
func WithMaxVersion(version byte) func(hc *HandshakeMgr) {
	return func(hc *HandshakeMgr) {
		hc.version = version
	}
}

func (h *HandshakeMgr) GetAuthData() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.authData
}

func (h *HandshakeMgr) GetRemoteStatic() *btcec.PublicKey {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.remoteStatic
}

func (h *HandshakeMgr) doHandshake() (*Machine, net.Conn, error) {
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

	if h.remoteStatic == nil {
		h.remoteStatic = h.cfg.RemoteStatic
	}

	if h.remoteStatic == nil || h.version < HandshakeVersion2 {
		noise, err = NewBrontideMachine(&BrontideMachineConfig{
			Initiator:           h.cfg.Initiator,
			HandshakePattern:    XXPattern,
			LocalStaticKey:      h.cfg.LocalStatic,
			PAKEPassphrase:      h.cfg.Passphrase,
			AuthData:            h.cfg.AuthData,
			MinHandshakeVersion: h.minVersion,
			MaxHandshakeVersion: h.version,
		})
		if err != nil {
			return nil, nil, err
		}

		conn1, err = h.cfg.GetConn(
			h.cfg.LocalStatic, h.remoteStatic, h.cfg.Passphrase,
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
		if h.cfg.Initiator {
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
		Initiator:           h.cfg.Initiator,
		HandshakePattern:    KKPattern,
		LocalStaticKey:      h.cfg.LocalStatic,
		RemoteStaticKey:     h.remoteStatic,
		AuthData:            h.cfg.AuthData,
		MinHandshakeVersion: h.minVersion,
		MaxHandshakeVersion: h.version,
	})
	if err != nil {
		return nil, nil, err
	}

	conn, err := h.cfg.GetConn(
		h.cfg.LocalStatic, h.remoteStatic, h.cfg.Passphrase,
	)
	if err != nil {
		return nil, nil, err
	}

	if err := performHandshake(conn, noise); err != nil {
		return nil, nil, err
	}

	// Once the KK handshake has completed successfully, we call the
	// callback function that will be used to persist the remote static key
	// so that any future handshake will jump directly to the KK step.
	h.cfg.OnRemoteStatic(h.remoteStatic)

	// Once we have persisted the remote static key, we can close conn1 if
	// this handshake switched from one conn to another.
	if conn1 != nil && h.cfg.CloseConn != nil {
		err := h.cfg.CloseConn(conn1)
		if err != nil {
			log.Errorf("error closing conn1: %v", err)
		}
	}

	if h.cfg.Initiator {
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
