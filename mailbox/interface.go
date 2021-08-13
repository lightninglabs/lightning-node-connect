package mailbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"google.golang.org/grpc/credentials"
)

const (
	// ProtocolName is the name of the protocol that's used for the
	// encrypted communication.
	ProtocolName = "XXeke_secp256k1+SPAKE2_CHACHAPOLY1305_SHA256"

	// ProtocolVersion is the version of the protocol used for the encrypted
	// communication.
	ProtocolVersion = 0
)

var (
	// byteOrder is the byte order that's used for serializing numeric
	// values when sending them over the wire.
	byteOrder = binary.BigEndian
)

// Addr is a type that implements the net.Addr interface for addresses involved
// in mailbox connections.
type Addr struct {
	// SID is the unique session ID used for the mailbox connection. It is
	// derived from the one-time connection password.
	SID [64]byte

	// Server is the mailbox server this connection is established over.
	Server string
}

// Network returns the network identifier for mailbox connections which is
// always 'mailbox'.
func (b *Addr) Network() string {
	return "mailbox"
}

// String returns a string representation of a mailbox address.
func (b *Addr) String() string {
	return fmt.Sprintf("%s:%x@%s", b.Network(), b.SID[:], b.Server)
}

var _ net.Addr = (*Addr)(nil)

// NoiseAddr is a type that implements the net.Addr interface for addresses
// involved in noise encrypted communication over mailbox connections.
type NoiseAddr struct {
	// PubKey is the EC public key of the noise participant of this address.
	PubKey *btcec.PublicKey

	// Server is the mailbox server this connection is established over.
	Server string
}

// Network returns the network identifier for noise connections which is always
// 'noise'.
func (b *NoiseAddr) Network() string {
	return "noise"
}

// String returns a string representation of a noise address.
func (b *NoiseAddr) String() string {
	if b.PubKey != nil {
		return fmt.Sprintf("%s:%x@%s", b.Network(),
			b.PubKey.SerializeCompressed(), b.Server)
	}

	return fmt.Sprintf("%s@%s", b.Network(), b.Server)
}

var _ net.Addr = (*NoiseAddr)(nil)

// controlConn is an interface that needs to be implemented by any concrete
// implementation of the Conn interface that wants to make use of the connKit
// struct.
type controlConn interface {
	ReceiveControlMsg(ControlMsg) error
	SendControlMsg(ControlMsg) error
}

// Conn is the main interface that any mailbox connection needs to implement.
type Conn interface {
	net.Conn
	controlConn
}

// connKit is a type that implements the common functionality imposed upon the
// mailbox connection by the net.Conn interface.
type connKit struct {
	ctx        context.Context
	serverAddr string
	impl       controlConn

	readDeadline  time.Time
	writeDeadline time.Time

	receiveSID [64]byte
	sendSID    [64]byte
}

// Read reads data from the underlying control connection.
//
// NOTE: This is part of the net.Conn interface.
func (k *connKit) Read(b []byte) (int, error) {
	data := NewMsgData(ProtocolVersion, nil)
	if err := k.impl.ReceiveControlMsg(data); err != nil {
		return 0, err
	}

	copy(b, data.Payload)
	return len(data.Payload), nil
}

// Write writes data to the underlying control connection.
//
// NOTE: This is part of the net.Conn interface.
func (k *connKit) Write(b []byte) (n int, err error) {
	data := NewMsgData(ProtocolVersion, b)
	if err := k.impl.SendControlMsg(data); err != nil {
		return 0, err
	}

	return len(b), nil
}

// LocalAddr returns the address of the local side of this connection.
//
// NOTE: This is part of the net.Conn interface.
func (k *connKit) LocalAddr() net.Addr {
	return &Addr{SID: k.sendSID, Server: k.serverAddr}
}

// RemoteAddr returns the address of the remote side of this connection.
//
// NOTE: This is part of the net.Conn interface.
func (k *connKit) RemoteAddr() net.Addr {
	return &Addr{SID: k.receiveSID, Server: k.serverAddr}
}

// SetDeadline sets the read and write deadlines associated with the connection.
// It is equivalent to calling both SetReadDeadline and SetWriteDeadline.
//
// NOTE: This is part of the net.Conn interface.
func (k *connKit) SetDeadline(t time.Time) error {
	if err := k.SetReadDeadline(t); err != nil {
		return err
	}
	return k.SetWriteDeadline(t)
}

// SetReadDeadline sets the deadline for future Read calls and any
// currently-blocked Read call.
//
// NOTE: This is part of the net.Conn interface.
func (k *connKit) SetReadDeadline(t time.Time) error {
	k.readDeadline = t

	return nil
}

// SetWriteDeadline sets the deadline for future Write calls and any
// currently-blocked Write call.
//
// NOTE: This is part of the net.Conn interface.
func (k *connKit) SetWriteDeadline(t time.Time) error {
	k.writeDeadline = t

	return nil
}

// ControlMsg is the interface that needs to be implemented by any message that
// is sent over a control connection.
type ControlMsg interface {
	ProtocolVersion() uint8
	Serialize() ([]byte, error)
	Deserialize([]byte) error
}

// MsgConnect is a message that is sent from the client to the server to signal
// a connection attempt. This message is somewhat analogous to a TCP SYN message
// and is needed to establish the low-level mailbox connection before the
// higher-level noise handshake can begin.
// TODO(elle): maybe remove? is handled on a lower level.
type MsgConnect struct {
	// version is the protocol version used for this message.
	version uint8
}

// NewMsgConnect creates a new MsgConnect message with the given version.
func NewMsgConnect(version uint8) *MsgConnect {
	return &MsgConnect{version: version}
}

// ProtocolVersion returns the protocol version used with this message.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgConnect) ProtocolVersion() uint8 {
	return m.version
}

// Serialize returns the binary wire format representation of this message.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgConnect) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := buf.WriteByte(m.version); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Deserialize reads the binary wire format representation from the given bytes
// and deserializes them into the message struct.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgConnect) Deserialize(b []byte) error {
	const expectedLength = 1
	if len(b) < expectedLength {
		return io.EOF
	}
	m.version = b[0]
	return nil
}

var _ ControlMsg = (*MsgConnect)(nil)

// MsgClientHello is a message that is sent from the client to the server
// to initialize the 2-way noise handshake directly after the low-level mailbox
// connection was established. It contains the (SPAKE2 masked) client public
// key that should be used in the ECDH operations of the noise protocol.
type MsgClientHello struct {
	// version is the protocol version used for this message.
	version uint8

	// PubKey is the client's public key (SPAKE2 masked)  that is being used
	// for the noise connection.
	PubKey *btcec.PublicKey
}

// NewMsgClientHello creates a new MsgClientHello message with the given
// version and public key.
func NewMsgClientHello(version uint8, pubKey *btcec.PublicKey) *MsgClientHello {
	return &MsgClientHello{
		version: version,
		PubKey:  pubKey,
	}
}

// ProtocolVersion returns the protocol version used with this message.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgClientHello) ProtocolVersion() uint8 {
	return m.version
}

// Serialize returns the binary wire format representation of this message.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgClientHello) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := buf.WriteByte(m.version); err != nil {
		return nil, err
	}
	if _, err := buf.Write(m.PubKey.SerializeCompressed()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Deserialize reads the binary wire format representation from the given bytes
// and deserializes them into the message struct.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgClientHello) Deserialize(b []byte) error {
	const expectedLength = 1 + 33
	if len(b) < expectedLength {
		return io.EOF
	}
	m.version = b[0]
	pubKey, err := btcec.ParsePubKey(b[1:], btcec.S256())
	if err != nil {
		return err
	}
	m.PubKey = pubKey
	return nil
}

var _ ControlMsg = (*MsgClientHello)(nil)

// MsgServerHello is a message that is sent from the server to the client to
// confirm the 2-way noise handshake and return the (SPAKE2 masked) server
// public key that should be used in the ECDH operations of the noise protocol
// and the encrypted authentication data to use for the application level gRPC
// calls.
type MsgServerHello struct {
	// version is the protocol version used for this message.
	version uint8

	// PubKey is the server's public key (SPAKE2 masked)  that is being used
	// for the noise connection.
	PubKey *btcec.PublicKey

	// AuthData is the encrypted authentication data that can be used by the
	// client to access the application level gRPC interface.
	AuthData []byte
}

// NewMsgServerHello creates a new MsgServerHello message with the given
// version, public key and authentication data.
func NewMsgServerHello(version uint8, pubKey *btcec.PublicKey,
	authData []byte) *MsgServerHello {

	return &MsgServerHello{
		version:  version,
		PubKey:   pubKey,
		AuthData: authData,
	}
}

// ProtocolVersion returns the protocol version used with this message.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgServerHello) ProtocolVersion() uint8 {
	return m.version
}

// Serialize returns the binary wire format representation of this message.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgServerHello) Serialize() ([]byte, error) {
	var buf bytes.Buffer

	if err := buf.WriteByte(m.version); err != nil {
		return nil, err
	}

	if _, err := buf.Write(m.PubKey.SerializeCompressed()); err != nil {
		return nil, err
	}

	payloadLen := uint32(len(m.AuthData))
	var lenBytes [4]byte
	byteOrder.PutUint32(lenBytes[:], payloadLen)
	if _, err := buf.Write(lenBytes[:]); err != nil {
		return nil, err
	}

	if payloadLen > 0 {
		if _, err := buf.Write(m.AuthData); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// Deserialize reads the binary wire format representation from the given bytes
// and deserializes them into the message struct.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgServerHello) Deserialize(b []byte) error {
	const baseLength = 1 + 33 + 4
	if len(b) < baseLength {
		return io.EOF
	}
	m.version = b[0]

	pubKey, err := btcec.ParsePubKey(b[1:33+1], btcec.S256())
	if err != nil {
		return err
	}
	m.PubKey = pubKey

	lenBytes := b[33+1 : baseLength]
	payloadLen := byteOrder.Uint32(lenBytes)
	if len(b) < baseLength+int(payloadLen) {
		return io.EOF
	}

	if payloadLen > 0 {
		m.AuthData = b[baseLength : baseLength+int(payloadLen)]
	}

	return nil
}

var _ ControlMsg = (*MsgServerHello)(nil)

// MsgData is a message that's being sent between server and client to transport
// higher-level application data. The payload is encrypted by the current noise
// protocol symmetric session key that's derived from the from the client's and
// server's public/private key pairs using ECDH. The payload in this case is
// HTTP/2 raw frame data.
type MsgData struct {
	// version is the protocol version used for this message.
	version uint8

	// Payload is the raw HTTP/2 frame data encrypted with the current noise
	// protocol symmetric session key.
	Payload []byte
}

// NewMsgData creates a new MsgData message with the given version and payload.
func NewMsgData(version uint8, payload []byte) *MsgData {
	return &MsgData{version: version, Payload: payload}
}

// ProtocolVersion returns the protocol version used with this message.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgData) ProtocolVersion() uint8 {
	return m.version
}

// Serialize returns the binary wire format representation of this message.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgData) Serialize() ([]byte, error) {
	var buf bytes.Buffer
	if err := buf.WriteByte(m.version); err != nil {
		return nil, err
	}
	payloadLen := uint32(len(m.Payload))
	var lenBytes [4]byte
	byteOrder.PutUint32(lenBytes[:], payloadLen)
	if _, err := buf.Write(lenBytes[:]); err != nil {
		return nil, err
	}

	if payloadLen > 0 {
		if _, err := buf.Write(m.Payload); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// Deserialize reads the binary wire format representation from the given bytes
// and deserializes them into the message struct.
//
// NOTE: This is part of the ControlMsg interface.
func (m *MsgData) Deserialize(b []byte) error {
	const baseLength = 1 + 4
	if len(b) < baseLength {
		return io.EOF
	}
	m.version = b[0]

	lenBytes := b[1:baseLength]
	payloadLen := byteOrder.Uint32(lenBytes)
	if len(b) < baseLength+int(payloadLen) {
		return io.EOF
	}

	if payloadLen > 0 {
		m.Payload = b[baseLength : baseLength+int(payloadLen)]
	}

	return nil
}

var _ ControlMsg = (*MsgData)(nil)

// AuthInfo is a type that implements the gRPC specific credentials.AuthInfo
// interface that's needed for implementing a secure client/server handshake.
type AuthInfo struct {
	credentials.CommonAuthInfo
}

// NewAuthInfo returns a new AuthInfo instance.
func NewAuthInfo() *AuthInfo {
	return &AuthInfo{
		CommonAuthInfo: credentials.CommonAuthInfo{
			SecurityLevel: credentials.PrivacyAndIntegrity,
		},
	}
}

// AuthType returns the type of this custom authentication scheme.
func (a *AuthInfo) AuthType() string {
	return ProtocolName
}
