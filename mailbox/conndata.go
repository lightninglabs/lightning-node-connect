package mailbox

import (
	"crypto/sha512"
	"sync"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/keychain"
)

// ConnectionData is the interface that needs to be implemented by any object
// that the Brontide Noise Machine can use to fetch and set connection data.
type ConnectionData interface {
	// LocalKey returns the local static key being used for the connection.
	LocalKey() keychain.SingleKeyECDH

	// RemoteKey returns the remote parties static key being used for the
	// connection.
	RemoteKey() *btcec.PublicKey

	// PassphraseEntropy returns the entropy bytes to be used for mask
	// the local ephemeral key during the first step of the Noise XX
	// handshake.
	PassphraseEntropy() []byte

	// AuthData returns the auth date we intend to send to our peer if we
	// are the responder or have received from our peer if we are the
	// initiator.
	AuthData() []byte

	// SetRemote processes and stores the remote parties static key.
	SetRemote(key *btcec.PublicKey) error

	// SetAuthData processes and stores the auth data received from the
	// remote party.
	SetAuthData(data []byte) error
}

var _ ConnectionData = (*ConnData)(nil)

// ConnData manages all the items needed for establishing a noise connection
// to a peer. This is an implementation of the ConnectionData interface with
// some extra methods that are customised for our specific usage of the Noise
// Machine and the underlying connection.
type ConnData struct {
	localKey          keychain.SingleKeyECDH
	remoteKey         *btcec.PublicKey
	passphraseEntropy []byte
	authData          []byte

	onRemoteStatic func(key *btcec.PublicKey) error
	onAuthData     func(data []byte) error

	mu sync.RWMutex
}

// NewConnData creates a new ConnData instance.
func NewConnData(localKey keychain.SingleKeyECDH, remoteKey *btcec.PublicKey,
	passphraseEntropy []byte, authData []byte,
	onRemoteStatic func(key *btcec.PublicKey) error,
	onAuthData func(data []byte) error) *ConnData {

	return &ConnData{
		localKey:          localKey,
		remoteKey:         remoteKey,
		passphraseEntropy: passphraseEntropy,
		authData:          authData,
		onRemoteStatic:    onRemoteStatic,
		onAuthData:        onAuthData,
	}
}

// LocalKey returns the local static key being used for the connection.
// NOTE: This is part of the ConnectionData interface.
func (s *ConnData) LocalKey() keychain.SingleKeyECDH {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.localKey
}

// RemoteKey returns the remote static key being used for the connection.
// NOTE: This is part of the ConnectionData interface.
func (s *ConnData) RemoteKey() *btcec.PublicKey {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.remoteKey
}

// PassphraseEntropy returns the entropy bytes we will use to mask our key
// during the first step of the Noise XX handshake.
// NOTE: This is part of the ConnectionData interface.
func (s *ConnData) PassphraseEntropy() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.passphraseEntropy
}

// AuthData returns the auth date we intend to send to our peer if we are the
// responder or have received from our peer if we are the initiator.
// NOTE: This is part of the ConnectionData interface.
func (s *ConnData) AuthData() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.authData
}

// SetRemote updates the remote static key we have stored and also calls the
// onRemoteStatic call back.
// NOTE: This is part of the ConnectionData interface.
func (s *ConnData) SetRemote(key *btcec.PublicKey) error {
	// Note that to avoid a possible deadlock situation here (since we don't
	// know what will be called in this callback), we don't hold the lock
	// during the call back execution.
	if s.onRemoteStatic != nil {
		if err := s.onRemoteStatic(key); err != nil {
			return err
		}
	}

	s.mu.Lock()
	s.remoteKey = key
	s.mu.Unlock()
	return nil
}

// SetAuthData calls the onAuthData callback with the given data and updates
// the auth data we have stored.
// NOTE: This is part of the ConnectionData interface.
func (s *ConnData) SetAuthData(data []byte) error {
	// Note that to avoid a possible deadlock situation here (since we don't
	// know what will be called in this callback), we don't hold the lock
	// during the call back execution.
	if s.onAuthData != nil {
		if err := s.onAuthData(data); err != nil {
			return err
		}
	}

	s.mu.Lock()
	s.authData = data
	s.mu.Unlock()
	return nil
}

// SID calculates the SID to be used given the presence of the remote static
// key. If the remote key is not present, then only the passphraseEntropy will be used
// to derive the SID. Otherwise, the ECDH operation on the remote and local key
// will be used.
func (s *ConnData) SID() ([64]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var entropy = s.passphraseEntropy
	if s.remoteKey != nil {
		hash, err := ecdh(s.remoteKey, s.localKey)
		if err != nil {
			return [64]byte{}, err
		}

		// Note that both the client and server will use the same HMAC
		// key because we want their stream IDs to be identical except
		// for the last bit which will be flipped depending on if it
		// is the send or receive stream.
		entropy, err = hmac256(hash, []byte("mailbox"))
		if err != nil {
			return [64]byte{}, err
		}
	}

	return sha512.Sum512(entropy), nil
}

// HandshakePattern returns the handshake pattern we should use for a noise
// handshake given knowledge of the remote static key.
func (s *ConnData) HandshakePattern() HandshakePattern {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.remoteKey == nil {
		return XXPattern
	}

	return KKPattern
}
