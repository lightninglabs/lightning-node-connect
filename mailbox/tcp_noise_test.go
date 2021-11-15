package mailbox

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"net"
	"testing"
	"testing/iotest"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/tor"
	"github.com/stretchr/testify/require"
)

var (
	pass     = []byte("top secret")
	passHash = sha256.Sum256(pass)
)

type maybeNetConn struct {
	conn net.Conn
	err  error
}

func makeListener(passphrase, authData []byte) (*Listener, net.Addr, error) {
	// First, generate the long-term private keys for the brontide listener.
	localPriv, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, nil, err
	}
	localKeyECDH := &keychain.PrivKeyECDH{PrivKey: localPriv}

	// Having a port of ":0" means a random port, and interface will be
	// chosen for our listener.
	addr := "localhost:0"

	// Our listener will be local, and the connection remote.
	listener, err := NewListener(passphrase, localKeyECDH, addr, authData)
	if err != nil {
		return nil, nil, err
	}

	netAddr, err := net.ResolveTCPAddr("tcp", listener.Addr().String())
	if err != nil {
		return nil, nil, err
	}

	return listener, netAddr, nil
}

func establishTestConnection(clientPass, serverPass,
	authData []byte) (net.Conn, net.Conn, func(), error) {

	listener, netAddr, err := makeListener(serverPass, authData)
	if err != nil {
		return nil, nil, nil, err
	}
	defer listener.Close()

	// Now, generate the long-term private keys remote end of the connection
	// within our test.
	remotePriv, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		return nil, nil, nil, err
	}
	remoteKeyECDH := &keychain.PrivKeyECDH{PrivKey: remotePriv}

	// Initiate a connection with a separate goroutine, and listen with our
	// main one. If both errors are nil, then encryption+auth was
	// successful.
	remoteConnChan := make(chan maybeNetConn, 1)
	go func() {
		remoteConn, err := Dial(
			remoteKeyECDH, netAddr, clientPass,
			tor.DefaultConnTimeout, net.DialTimeout,
		)
		remoteConnChan <- maybeNetConn{remoteConn, err}
	}()

	localConnChan := make(chan maybeNetConn, 1)
	go func() {
		localConn, err := listener.Accept()
		localConnChan <- maybeNetConn{localConn, err}
	}()

	remote := <-remoteConnChan
	if remote.err != nil {
		return nil, nil, nil, remote.err
	}

	local := <-localConnChan
	if local.err != nil {
		return nil, nil, nil, local.err
	}

	cleanUp := func() {
		local.conn.Close()
		remote.conn.Close()
	}

	return local.conn, remote.conn, cleanUp, nil
}

func TestConnectionCorrectness(t *testing.T) {
	authData := []byte("fake macaroon")

	// Create a test connection, grabbing either side of the connection
	// into local variables. If the initial crypto handshake fails, then
	// we'll get a non-nil error here.
	localConn, remoteConn, cleanUp, err := establishTestConnection(
		passHash[:], passHash[:], authData,
	)
	if err != nil {
		t.Fatalf("unable to establish test connection: %v", err)
	}
	defer cleanUp()

	// At this point, since we specified the auth data above, the client
	// should now know of this information.
	noiseConnLocal := localConn.(*NoiseConn)
	noiseConnRemote := remoteConn.(*NoiseConn)
	require.Equal(
		t, noiseConnLocal.noise.authData, noiseConnRemote.noise.authData,
	)

	// Test out some message full-message reads.
	for i := 0; i < 10; i++ {
		msg := []byte(fmt.Sprintf("hello%d", i))

		if _, err := localConn.Write(msg); err != nil {
			t.Fatalf("remote conn failed to write: %v", err)
		}

		readBuf := make([]byte, len(msg))
		if _, err := remoteConn.Read(readBuf); err != nil {
			t.Fatalf("local conn failed to read: %v", err)
		}

		if !bytes.Equal(readBuf, msg) {
			t.Fatalf("messages don't match, %v vs %v",
				string(readBuf), string(msg))
		}
	}

	// Now try incremental message reads. This simulates first writing a
	// message header, then a message body.
	outMsg := []byte("hello world")
	if _, err := localConn.Write(outMsg); err != nil {
		t.Fatalf("remote conn failed to write: %v", err)
	}

	readBuf := make([]byte, len(outMsg))
	if _, err := remoteConn.Read(readBuf[:len(outMsg)/2]); err != nil {
		t.Fatalf("local conn failed to read: %v", err)
	}
	if _, err := remoteConn.Read(readBuf[len(outMsg)/2:]); err != nil {
		t.Fatalf("local conn failed to read: %v", err)
	}

	if !bytes.Equal(outMsg, readBuf) {
		t.Fatalf("messages don't match, %v vs %v",
			string(readBuf), string(outMsg))
	}
}

// TestConnectionCorrectnessWrongPassphrase tests that if a client attempts to
// connect to a server w/ the wrong passphrase, then the connection is
// abandoned after the first step.
func TestConnectionCorrectnessWrongPassphrase(t *testing.T) {
	// Create a test connection, grabbing either side of the connection
	// into local variables. If the initial crypto handshake fails, then
	// we'll get a non-nil error here.
	_, _, _, err := establishTestConnection(
		[]byte("wrong pass"), passHash[:], nil,
	)

	// The client is trying to connect to the server using the wrong PAKE
	// pass phrase, should error out here as the ActOne message should be
	// rejected by the server.
	require.Error(t, err)
}

// TestConecurrentHandshakes verifies the listener's ability to not be blocked
// by other pending handshakes. This is tested by opening multiple tcp
// connections with the listener, without completing any of the brontide acts.
// The test passes if real brontide dialer connects while the others are
// stalled.
func TestConcurrentHandshakes(t *testing.T) {
	listener, netAddr, err := makeListener(passHash[:], nil)
	if err != nil {
		t.Fatalf("unable to create listener connection: %v", err)
	}
	defer listener.Close()

	const nblocking = 5

	// Open a handful of tcp connections, that do not complete any steps of
	// the brontide handshake.
	connChan := make(chan maybeNetConn)
	for i := 0; i < nblocking; i++ {
		go func() {
			conn, err := net.Dial("tcp", listener.Addr().String())
			connChan <- maybeNetConn{conn, err}
		}()
	}

	// Receive all connections/errors from our blocking tcp dials. We make a
	// pass to gather all connections and errors to make sure we defer the
	// calls to Close() on all successful connections.
	tcpErrs := make([]error, 0, nblocking)
	for i := 0; i < nblocking; i++ {
		result := <-connChan
		if result.conn != nil {
			defer result.conn.Close()
		}
		if result.err != nil {
			tcpErrs = append(tcpErrs, result.err)
		}
	}
	for _, tcpErr := range tcpErrs {
		if tcpErr != nil {
			t.Fatalf("unable to tcp dial listener: %v", tcpErr)
		}
	}

	// Now, construct a new private key and use the brontide dialer to
	// connect to the listener.
	remotePriv, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatalf("unable to generate private key: %v", err)
	}
	remoteKeyECDH := &keychain.PrivKeyECDH{PrivKey: remotePriv}

	go func() {
		remoteConn, err := Dial(
			remoteKeyECDH, netAddr, passHash[:],
			tor.DefaultConnTimeout, net.DialTimeout,
		)
		connChan <- maybeNetConn{remoteConn, err}
	}()

	// This connection should be accepted without error, as the brontide
	// connection should bypass stalled tcp connections.
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("unable to accept dial: %v", err)
	}
	defer conn.Close()

	result := <-connChan
	if result.err != nil {
		t.Fatalf("unable to dial %v: %v", netAddr, result.err)
	}
	result.conn.Close()
}

func TestMaxPayloadLength(t *testing.T) {
	t.Parallel()

	b := Machine{}
	b.split()

	// Create a payload that's only *slightly* above the maximum allotted
	// payload length.
	payloadToReject := make([]byte, math.MaxUint16+1)

	// A write of the payload generated above to the state machine should
	// be rejected as it's over the max payload length.
	err := b.WriteMessage(payloadToReject)
	if err != ErrMaxMessageLengthExceeded {
		t.Fatalf("payload is over the max allowed length, the write " +
			"should have been rejected")
	}

	// Generate another payload which should be accepted as a valid
	// payload.
	payloadToAccept := make([]byte, math.MaxUint16-1)
	if err := b.WriteMessage(payloadToAccept); err != nil {
		t.Fatalf("write for payload was rejected, should have been " +
			"accepted")
	}

	// Generate a final payload which is only *slightly* above the max payload length
	// when the MAC is accounted for.
	payloadToReject = make([]byte, math.MaxUint16+1)

	// This payload should be rejected.
	err = b.WriteMessage(payloadToReject)
	if err != ErrMaxMessageLengthExceeded {
		t.Fatalf("payload is over the max allowed length, the write " +
			"should have been rejected")
	}
}

func TestWriteMessageChunking(t *testing.T) {
	// Create a test connection, grabbing either side of the connection
	// into local variables. If the initial crypto handshake fails, then
	// we'll get a non-nil error here.
	localConn, remoteConn, cleanUp, err := establishTestConnection(
		passHash[:], passHash[:], nil,
	)
	if err != nil {
		t.Fatalf("unable to establish test connection: %v", err)
	}
	defer cleanUp()

	// Attempt to write a message which is over 3x the max allowed payload
	// size.
	largeMessage := bytes.Repeat([]byte("kek"), math.MaxUint16*3)

	// Launch a new goroutine to write the large message generated above in
	// chunks. We spawn a new goroutine because otherwise, we may block as
	// the kernel waits for the buffer to flush.
	errCh := make(chan error)
	go func() {
		defer close(errCh)

		bytesWritten, err := localConn.Write(largeMessage)
		if err != nil {
			errCh <- fmt.Errorf("unable to write message: %v", err)
			return
		}

		// The entire message should have been written out to the remote
		// connection.
		if bytesWritten != len(largeMessage) {
			errCh <- fmt.Errorf("bytes not fully written")
			return
		}
	}()

	// Attempt to read the entirety of the message generated above.
	buf := make([]byte, len(largeMessage))
	if _, err := io.ReadFull(remoteConn, buf); err != nil {
		t.Fatalf("unable to read message: %v", err)
	}

	err = <-errCh
	if err != nil {
		t.Fatal(err)
	}

	// Finally, the message the remote end of the connection received
	// should be identical to what we sent from the local connection.
	if !bytes.Equal(buf, largeMessage) {
		t.Fatalf("bytes don't match")
	}
}

// timeoutWriter wraps an io.Writer and throws an iotest.ErrTimeout after
// writing n bytes.
type timeoutWriter struct {
	w io.Writer
	n int64
}

func NewTimeoutWriter(w io.Writer, n int64) io.Writer {
	return &timeoutWriter{w, n}
}

func (t *timeoutWriter) Write(p []byte) (int, error) {
	n := len(p)
	if int64(n) > t.n {
		n = int(t.n)
	}
	n, err := t.w.Write(p[:n])
	t.n -= int64(n)
	if err == nil && t.n == 0 {
		return n, iotest.ErrTimeout
	}
	return n, err
}

const payloadSize = 10

type flushChunk struct {
	errAfter int64
	expN     int
	expErr   error
}

type flushTest struct {
	name   string
	chunks []flushChunk
}

var flushTests = []flushTest{
	{
		name: "partial header write",
		chunks: []flushChunk{
			// Write 18-byte header in two parts, 16 then 2.
			{
				errAfter: encHeaderSize - 2,
				expN:     0,
				expErr:   iotest.ErrTimeout,
			},
			{
				errAfter: 2,
				expN:     0,
				expErr:   iotest.ErrTimeout,
			},
			// Write payload and MAC in one go.
			{
				errAfter: -1,
				expN:     payloadSize,
			},
		},
	},
	{
		name: "full payload then full mac",
		chunks: []flushChunk{
			// Write entire header and entire payload w/o MAC.
			{
				errAfter: encHeaderSize + payloadSize,
				expN:     payloadSize,
				expErr:   iotest.ErrTimeout,
			},
			// Write the entire MAC.
			{
				errAfter: -1,
				expN:     0,
			},
		},
	},
	{
		name: "payload-only, straddle, mac-only",
		chunks: []flushChunk{
			// Write header and all but last byte of payload.
			{
				errAfter: encHeaderSize + payloadSize - 1,
				expN:     payloadSize - 1,
				expErr:   iotest.ErrTimeout,
			},
			// Write last byte of payload and first byte of MAC.
			{
				errAfter: 2,
				expN:     1,
				expErr:   iotest.ErrTimeout,
			},
			// Write 10 bytes of the MAC.
			{
				errAfter: 10,
				expN:     0,
				expErr:   iotest.ErrTimeout,
			},
			// Write the remaining 5 MAC bytes.
			{
				errAfter: -1,
				expN:     0,
			},
		},
	},
}

// TestFlush asserts a Machine's ability to handle timeouts during Flush that
// cause partial writes, and that the machine can properly resume writes on
// subsequent calls to Flush.
func TestFlush(t *testing.T) {
	// Run each test individually, to assert that they pass in isolation.
	for _, test := range flushTests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var (
				w bytes.Buffer
				b Machine
			)
			b.split()
			testFlush(t, test, &b, &w)
		})
	}

	// Finally, run the tests serially as if all on one connection.
	t.Run("flush serial", func(t *testing.T) {
		var (
			w bytes.Buffer
			b Machine
		)
		b.split()
		for _, test := range flushTests {
			testFlush(t, test, &b, &w)
		}
	})
}

// testFlush buffers a message on the Machine, then flushes it to the io.Writer
// in chunks. Once complete, a final call to flush is made to assert that Write
// is not called again.
func testFlush(t *testing.T, test flushTest, b *Machine, w io.Writer) {
	payload := make([]byte, payloadSize)
	if err := b.WriteMessage(payload); err != nil {
		t.Fatalf("unable to write message: %v", err)
	}

	for _, chunk := range test.chunks {
		assertFlush(t, b, w, chunk.errAfter, chunk.expN, chunk.expErr)
	}

	// We should always be able to call Flush after a message has been
	// successfully written, and it should result in a NOP.
	assertFlush(t, b, w, 0, 0, nil)
}

// assertFlush flushes a chunk to the passed io.Writer. If n >= 0, a
// timeoutWriter will be used the flush should stop with iotest.ErrTimeout after
// n bytes. The method asserts that the returned error matches expErr and that
// the number of bytes written by Flush matches expN.
func assertFlush(t *testing.T, b *Machine, w io.Writer, n int64, expN int,
	expErr error) {

	t.Helper()

	if n >= 0 {
		w = NewTimeoutWriter(w, n)
	}
	nn, err := b.Flush(w)
	if err != expErr {
		t.Fatalf("expected flush err: %v, got: %v", expErr, err)
	}
	if nn != expN {
		t.Fatalf("expected n: %d, got: %d", expN, nn)
	}
}
