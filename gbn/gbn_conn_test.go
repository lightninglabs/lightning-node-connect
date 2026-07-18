package gbn

import (
	"bytes"
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormal(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s1Write := func(ctx context.Context, b []byte) error {
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	server, client, cleanup := setUpClientServerConns(
		t, 2, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanup()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")

	go func() {
		err := server.Send(payload1)
		require.NoError(t, err)

		err = server.Send(payload2)
		require.NoError(t, err)
	}()

	msg, err := client.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))

	msg, err = client.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))
}

// TestServerHandshakeTimeout ensures that the SetRecvTimout properly exits out
// of the Recv function if the timout has passed before receiving anything.
// This is useful in the case of a handshake on the layer above GBN. The test
// does the following: We kick off a handshake but we ensure that the clients
// SYNACK message delays enough for the server to time out the handshake and
// start again by waiting for SYN. The client, however, will think the handshake
// has completed and so will go into normal message sending operation mode and
// so will call Recv or Send which will hang indefinitely unless we set a
// timeout.
func TestServerHandshakeTimeout(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	// Client Read
	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	// Server write
	s1Write := func(ctx context.Context, b []byte) error { //nolint:unparam
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	// Server Read
	var (
		serverReadCount = 1
		countMu         sync.Mutex
	)
	s2Read := func(ctx context.Context) ([]byte, error) { //nolint:unparam
		countMu.Lock()
		defer func() {
			serverReadCount++
			countMu.Unlock()
		}()

		select {
		case val := <-s2Chan:
			// Let the client SYNACK message delay for a bit in
			// order to ensure that the server times it out.
			if serverReadCount == 2 {
				time.Sleep(defaultHandshakeTimeout * 2)
			}

			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	// Client write
	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	var (
		server *GoBackNConn
		wg     sync.WaitGroup
	)
	defer func() {
		if server != nil {
			server.Close()
		}
	}()

	payload1 := []byte("payload 1")

	wg.Add(1)
	go func() {
		defer wg.Done()

		var err error
		server, err = NewServerConn(ctx, s1Write, s2Read)
		require.NoError(t, err)

		err = server.Send(payload1)
		require.NoError(t, err)
	}()

	// Give the server time to be ready for the handshake
	time.Sleep(time.Millisecond * 200)

	client, err := NewClientConn(ctx, 10, s2Write, s1Read)
	require.NoError(t, err)
	defer client.Close()

	client.SetRecvTimeout(defaultHandshakeTimeout)

	_, err = client.Recv()
	require.ErrorIs(t, err, errRecvTimeout)

	cancel()
	wg.Wait()
}

func TestDroppedMessage(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	var (
		count   int
		countMu sync.Mutex
	)
	s1Write := func(ctx context.Context, b []byte) error {
		countMu.Lock()
		defer func() {
			count++
			countMu.Unlock()
		}()

		// Drop the first message (after handshake)
		if count == 2 {
			return nil
		}

		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanUp := setUpClientServerConns(
		t, 2, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanUp()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")

	go func() {
		err := p1.Send(payload1)
		require.NoError(t, err)

		err = p1.Send(payload2)
		require.NoError(t, err)
	}()

	msg, err := p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))
}

func TestDroppedACKs(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s1Write := func(ctx context.Context, b []byte) error {
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	var (
		count   int
		countMu sync.Mutex
		n       uint8 = 2
	)
	s2Write := func(ctx context.Context, b []byte) error {
		countMu.Lock()
		defer func() {
			count++
			countMu.Unlock()
		}()

		// Drop the first n messages (after handshake)
		if count > 2 && count < int(n+2) {
			return nil
		}

		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanUp := setUpClientServerConns(
		t, n, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanUp()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")
	payload3 := []byte("payload 3")

	go func() {
		err := p1.Send(payload1)
		require.NoError(t, err)

		err = p1.Send(payload2)
		require.NoError(t, err)

		err = p1.Send(payload3)
		require.NoError(t, err)
	}()

	msg, err := p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload3))
}

func TestReceiveDuplicateMessages(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	// duplicate messages (not including handshake)
	var (
		count   int
		countMu sync.Mutex
	)
	s1Write := func(ctx context.Context, b []byte) error {
		countMu.Lock()
		defer func() {
			count++
			countMu.Unlock()
		}()

		s1Chan <- b

		if count < 1 {
			return nil
		}
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanUp := setUpClientServerConns(
		t, 2, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanUp()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")

	go func() {
		err := p1.Send(payload1)
		require.NoError(t, err)

		err = p1.Send(payload2)
		require.NoError(t, err)
	}()

	msg, err := p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))
}

func TestReceiveDuplicateDataAndACKs(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	// duplicate messages (not including handshake)
	var (
		count   int
		countMu sync.Mutex
	)
	s1Write := func(ctx context.Context, b []byte) error {
		countMu.Lock()
		defer func() {
			count++
			countMu.Unlock()
		}()

		s1Chan <- b

		if count < 1 {
			return nil
		}
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	// duplicate messages (not including handshake)
	var (
		count2   int
		count2Mu sync.Mutex
	)
	s2Write := func(ctx context.Context, b []byte) error {
		count2Mu.Lock()
		defer func() {
			count2++
			count2Mu.Unlock()
		}()

		s2Chan <- b

		if count2 < 2 {
			return nil
		}

		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanUp := setUpClientServerConns(
		t, 2, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanUp()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")

	go func() {
		err := p1.Send(payload1)
		require.NoError(t, err)

		err = p1.Send(payload2)
		require.NoError(t, err)
	}()

	msg, err := p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))
}

func TestBidirectional(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s1Write := func(ctx context.Context, b []byte) error {
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanUp := setUpClientServerConns(
		t, 2, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanUp()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")
	payload3 := []byte("payload 3")
	payload4 := []byte("payload 4")

	go func() {
		err := p1.Send(payload1)
		require.NoError(t, err)

		err = p1.Send(payload2)
		require.NoError(t, err)
	}()

	go func() {
		err := p2.Send(payload3)
		require.NoError(t, err)

		err = p2.Send(payload4)
		require.NoError(t, err)
	}()

	msg, err := p1.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload3))

	msg, err = p1.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload4))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))
}

func TestSendNBeforeNeedingAck(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s1Write := func(ctx context.Context, b []byte) error {
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanUp := setUpClientServerConns(
		t, 2, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanUp()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")

	err := p1.Send(payload1)
	require.NoError(t, err)

	err = p1.Send(payload2)
	require.NoError(t, err)

	msg, err := p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))
}

func TestDropFirstNPackets(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	var (
		n       uint8 = 3
		count   uint8
		countMu sync.Mutex
	)
	s1Write := func(ctx context.Context, b []byte) error {
		countMu.Lock()
		defer func() {
			count++
			countMu.Unlock()
		}()

		// drop first non-handshake packet
		if count > 0 && count < n+1 {
			return nil
		}

		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanUp := setUpClientServerConns(
		t, n, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanUp()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")
	payload3 := []byte("payload 3")

	err := p1.Send(payload1)
	require.NoError(t, err)

	err = p1.Send(payload2)
	require.NoError(t, err)

	err = p1.Send(payload3)
	require.NoError(t, err)

	msg, err := p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload3))
}

func TestBidirectional2(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s1Write := func(ctx context.Context, b []byte) error {
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanUp := setUpClientServerConns(
		t, 2, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanUp()

	payload1 := []byte("client hello")
	payload2 := []byte("server hello")
	payload3 := []byte("client data 1")
	payload4 := []byte("client data 2")
	payload5 := []byte("client data 3")
	payload6 := []byte("server data 1")
	payload7 := []byte("server data 2")
	payload8 := []byte("client bye")
	payload9 := []byte("server bye")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Server
		msg, err := p2.Recv()
		require.NoError(t, err)
		require.True(t, bytes.Equal(msg, payload1))

		err = p2.Send(payload2)
		require.NoError(t, err)

		msg, err = p2.Recv()
		require.NoError(t, err)
		require.True(t, bytes.Equal(msg, payload3))

		msg, err = p2.Recv()
		require.NoError(t, err)
		require.True(t, bytes.Equal(msg, payload4))

		msg, err = p2.Recv()
		require.NoError(t, err)
		require.True(t, bytes.Equal(msg, payload5))

		err = p2.Send(payload6)
		require.NoError(t, err)

		err = p2.Send(payload7)
		require.NoError(t, err)

		msg, err = p2.Recv()
		require.NoError(t, err)
		require.True(t, bytes.Equal(msg, payload8))

		err = p2.Send(payload9)
		require.NoError(t, err)
	}()

	// Client
	err := p1.Send(payload1)
	require.NoError(t, err)

	msg, err := p1.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))

	err = p1.Send(payload3)
	require.NoError(t, err)

	err = p1.Send(payload4)
	require.NoError(t, err)

	err = p1.Send(payload5)
	require.NoError(t, err)

	msg, err = p1.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload6))

	msg, err = p1.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload7))

	err = p1.Send(payload8)
	require.NoError(t, err)

	msg, err = p1.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload9))

	wg.Wait()
}

func TestSendingIsNonBlockingUpToN(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	// drop every 3rd packet (after handshake)
	var (
		count   int
		countMu sync.Mutex
	)
	s1Write := func(ctx context.Context, b []byte) error {
		countMu.Lock()
		defer func() {
			count++
			countMu.Unlock()
		}()

		if count != 0 && count%3 == 0 {
			return nil
		}

		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanUp := setUpClientServerConns(
		t, 2, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanUp()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")
	payload3 := []byte("payload 3")
	payload4 := []byte("payload 4")

	go func() {
		err := p1.Send(payload1)
		require.NoError(t, err)

		err = p1.Send(payload2)
		require.NoError(t, err)

		msg, err := p1.Recv()
		require.NoError(t, err)
		require.True(t, bytes.Equal(msg, payload3))

		err = p1.Send(payload4)
		require.NoError(t, err)
	}()

	err := p2.Send(payload3)
	require.NoError(t, err)

	msg, err := p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload2))

	msg, err = p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload4))
}

func TestSendingLargeNumberOfMessages(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s1Write := func(ctx context.Context, b []byte) error {
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanup := setUpClientServerConns(
		t, 100, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanup()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")

	done := make(chan struct{})
	go func() {
		for i := 0; i <= 10000; i++ {
			err := p1.Send(payload1)
			require.NoError(t, err)

			msg, err := p1.Recv()
			require.NoError(t, err)
			require.True(t, bytes.Equal(msg, payload2))
		}
		close(done)
	}()

	for i := 0; i <= 10000; i++ {
		err := p2.Send(payload2)
		require.NoError(t, err)

		msg, err := p2.Recv()
		require.NoError(t, err)
		require.True(t, bytes.Equal(msg, payload1))
	}
	<-done
}

func TestResendAfterTimeout(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s1Write := func(ctx context.Context, b []byte) error {
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, cleanup := setUpClientServerConns(
		t, 100, s1Read, s2Read, s2Write, s1Write,
	)
	defer cleanup()

	payload1 := []byte("payload 1")

	err := p1.Send(payload1)
	require.NoError(t, err)

	msg, err := p2.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))
}

func TestPayloadSplitting(t *testing.T) {
	s1Chan := make(chan []byte, 10)
	s2Chan := make(chan []byte, 10)

	s1Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s1Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s1Write := func(ctx context.Context, b []byte) error {
		select {
		case s1Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	s2Read := func(ctx context.Context) ([]byte, error) {
		select {
		case val := <-s2Chan:
			return val, nil
		case <-ctx.Done():
		}
		return nil, nil
	}

	s2Write := func(ctx context.Context, b []byte) error {
		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	maxPayloadSize := 1000
	payload1 := make([]byte, 4000)
	rand.Read(payload1)

	server, client, cleanup := setUpClientServerConns(
		t, 2, s1Read, s2Read, s2Write, s1Write,
		WithMaxSendSize(maxPayloadSize),
	)
	defer cleanup()

	go func() {
		err := server.Send(payload1)
		require.NoError(t, err)
	}()

	msg, err := client.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload1))
}

// delayedStream wraps an in-memory channel with an optional delivery delay.
// The helper lets the compatibility tests inject RTT without changing the
// production transport code paths.
type delayedStream struct {
	ch chan []byte

	mu         sync.RWMutex
	delay      time.Duration
	pingNotify chan time.Time
	finNotify  chan time.Time
}

// newDelayedStream creates a delayedStream backed by a buffered channel.
func newDelayedStream(size int) *delayedStream {
	return &delayedStream{
		ch: make(chan []byte, size),
	}
}

// setDelay updates the one-way delivery delay applied to future writes.
func (s *delayedStream) setDelay(delay time.Duration) {
	s.mu.Lock()
	s.delay = delay
	s.mu.Unlock()
}

// notifyPing returns a channel that receives a timestamp whenever a keepalive
// ping is written to this stream.
func (s *delayedStream) notifyPing() <-chan time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pingNotify == nil {
		s.pingNotify = make(chan time.Time, 10)
	}

	return s.pingNotify
}

// notifyFIN returns a channel that receives a timestamp whenever a FIN is
// written to this stream.
func (s *delayedStream) notifyFIN() <-chan time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.finNotify == nil {
		s.finNotify = make(chan time.Time, 10)
	}

	return s.finNotify
}

// read returns the next queued payload, or exits early when the context ends.
func (s *delayedStream) read(ctx context.Context) ([]byte, error) {
	select {
	case val := <-s.ch:
		return val, nil
	case <-ctx.Done():
	}

	return nil, nil
}

// write applies the configured delivery delay before enqueueing a payload.
// The payload is copied first so later mutations by the caller don't affect
// what the peer observes.
func (s *delayedStream) write(ctx context.Context, b []byte) error {
	s.mu.RLock()
	delay := s.delay
	pingNotify := s.pingNotify
	finNotify := s.finNotify
	s.mu.RUnlock()

	if pingNotify != nil || finNotify != nil {
		msg, err := Deserialize(b)
		if err == nil {
			switch pkt := msg.(type) {
			case *PacketData:
				if pingNotify != nil && pkt.IsPing {
					select {
					case pingNotify <- time.Now():
					default:
					}
				}

			case *PacketFIN:
				if finNotify != nil {
					select {
					case finNotify <- time.Now():
					default:
					}
				}
			}
		}
	}

	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil
		}
	}

	payload := append([]byte(nil), b...)

	select {
	case s.ch <- payload:
		return nil
	case <-ctx.Done():
	}

	return nil
}

// latencyHarness wires together the two one-way streams used by the test
// client/server pair so RTT can be controlled independently from test logic.
type latencyHarness struct {
	clientToServer *delayedStream
	serverToClient *delayedStream
}

// newLatencyHarness creates a harness with separate streams for both
// directions of the connection under test.
func newLatencyHarness(buffer int) *latencyHarness {
	return &latencyHarness{
		clientToServer: newDelayedStream(buffer),
		serverToClient: newDelayedStream(buffer),
	}
}

// setRTT splits a target round-trip time across the two one-way streams.
// This keeps the transport symmetric unless a test explicitly chooses
// otherwise.
func (h *latencyHarness) setRTT(rtt time.Duration) {
	clientDelay := rtt / 2
	serverDelay := rtt - clientDelay

	h.clientToServer.setDelay(clientDelay)
	h.serverToClient.setDelay(serverDelay)
}

// clientRead exposes the client-side receive function expected by GBN.
func (h *latencyHarness) clientRead(ctx context.Context) ([]byte, error) {
	return h.serverToClient.read(ctx)
}

// clientWrite exposes the client-side send function expected by GBN.
func (h *latencyHarness) clientWrite(ctx context.Context, b []byte) error {
	return h.clientToServer.write(ctx, b)
}

// serverRead exposes the server-side receive function expected by GBN.
func (h *latencyHarness) serverRead(ctx context.Context) ([]byte, error) {
	return h.clientToServer.read(ctx)
}

// serverWrite exposes the server-side send function expected by GBN.
func (h *latencyHarness) serverWrite(ctx context.Context, b []byte) error {
	return h.serverToClient.write(ctx, b)
}

// observeClientPing returns notifications for keepalive pings sent by the
// client.
func (h *latencyHarness) observeClientPing() <-chan time.Time {
	return h.clientToServer.notifyPing()
}

// observeServerPing returns notifications for keepalive pings sent by the
// server.
func (h *latencyHarness) observeServerPing() <-chan time.Time {
	return h.serverToClient.notifyPing()
}

// observeClientFIN returns notifications for FIN packets sent by the client.
func (h *latencyHarness) observeClientFIN() <-chan time.Time {
	return h.clientToServer.notifyFIN()
}

// observeServerFIN returns notifications for FIN packets sent by the server.
func (h *latencyHarness) observeServerFIN() <-chan time.Time {
	return h.serverToClient.notifyFIN()
}

// setSmoothedRTT seeds the timeout manager with RTT data so the tests can
// exercise the dynamic pong path deterministically without waiting for a long
// training phase.
func setSmoothedRTT(conn *GoBackNConn, rtt time.Duration) {
	conn.timeoutManager.mu.Lock()
	conn.timeoutManager.smoothedRTT = rtt
	conn.timeoutManager.rttInitialized = true
	conn.timeoutManager.mu.Unlock()
}

// setUpMixedClientServerConns creates a client/server pair with independent
// option sets. This mirrors real mixed-version deployments where each side
// chooses its own local timeout policy.
func setUpMixedClientServerConns(t *testing.T, n uint8, harness *latencyHarness,
	clientOpts, serverOpts []Option) (*GoBackNConn, *GoBackNConn, func()) {

	t.Helper()

	var (
		server *GoBackNConn
		wg     sync.WaitGroup
		srvErr error
	)

	ctx := context.Background()

	wg.Add(1)
	go func() {
		defer wg.Done()

		server, srvErr = NewServerConn(
			ctx, harness.serverWrite, harness.serverRead,
			serverOpts...,
		)
	}()

	time.Sleep(200 * time.Millisecond)

	client, err := NewClientConn(
		ctx, n, harness.clientWrite, harness.clientRead, clientOpts...,
	)
	require.NoError(t, err)

	wg.Wait()
	require.NoError(t, srvErr)

	return server, client, func() {
		client.Close()
		server.Close()
	}
}

// requireOneWayPayload asserts that a single payload can be sent from one peer
// to the other and that the sender observes no asynchronous send error.
func requireOneWayPayload(t *testing.T, sender, receiver *GoBackNConn,
	payload []byte) {

	t.Helper()

	sendErrCh := make(chan error, 1)
	go func() {
		sendErrCh <- sender.Send(payload)
	}()

	msg, err := receiver.Recv()
	require.NoError(t, err)
	require.True(t, bytes.Equal(msg, payload))

	require.NoError(t, <-sendErrCh)
}

// requireConnClosed polls Recv until the connection exits for any reason other
// than the short probe timeout used by the helper itself.
func requireConnClosed(t *testing.T, conn *GoBackNConn, timeout time.Duration) {
	t.Helper()

	conn.SetRecvTimeout(10 * time.Millisecond)

	require.Eventually(t, func() bool {
		_, err := conn.Recv()

		return err != nil && err != errRecvTimeout
	}, timeout, 10*time.Millisecond)
}

// requirePingSent waits for a keepalive ping to be emitted on a transport
// stream and returns the send timestamp observed by the harness.
func requirePingSent(t *testing.T, pingChan <-chan time.Time,
	timeout time.Duration) time.Time {

	t.Helper()

	select {
	case pingAt := <-pingChan:
		return pingAt

	case <-time.After(timeout):
		t.Fatalf("expected keepalive ping within %v", timeout)

		return time.Time{}
	}
}

// waitForConnClosed measures how long it takes before Recv observes the
// connection closing for a reason other than the helper's probe timeout.
func waitForConnClosed(t *testing.T, conn *GoBackNConn,
	timeout time.Duration) time.Duration {

	t.Helper()

	conn.SetRecvTimeout(5 * time.Millisecond)

	start := time.Now()
	deadline := start.Add(timeout)

	for time.Now().Before(deadline) {
		_, err := conn.Recv()
		if err != nil && err != errRecvTimeout {
			return time.Since(start)
		}
	}

	t.Fatalf("expected connection to close within %v", timeout)

	return 0
}

// requireConnStaysOpen verifies that the connection does not close during the
// provided interval.
func requireConnStaysOpen(t *testing.T, conn *GoBackNConn,
	duration time.Duration) {

	t.Helper()

	conn.SetRecvTimeout(5 * time.Millisecond)
	defer conn.SetRecvTimeout(DefaultRecvTimeout)

	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		_, err := conn.Recv()
		if err != nil && err != errRecvTimeout {
			t.Fatalf("expected connection to stay open for %v, got: %v",
				duration, err)
		}
	}
}

// oldServerCompatOptions returns the pre-fix keepalive settings used by the
// historical server implementation.
func oldServerCompatOptions() []Option {
	return []Option{
		WithTimeoutOptions(
			WithKeepalivePing(5*time.Second, 3*time.Second),
		),
	}
}

// newClientCompatOptions returns the post-fix client settings with dynamic
// pong enabled and capped by the local client ping interval.
func newClientCompatOptions() []Option {
	return []Option{
		WithTimeoutOptions(
			WithKeepalivePing(10*time.Second, 5*time.Second),
			WithDynamicPongTimeout(3, 10*time.Second),
		),
	}
}

// newServerCompatOptions returns the post-fix server settings with dynamic
// pong enabled and capped by the local server ping interval.
func newServerCompatOptions() []Option {
	return []Option{
		WithTimeoutOptions(
			WithKeepalivePing(8*time.Second, 5*time.Second),
			WithDynamicPongTimeout(3, 8*time.Second),
		),
	}
}

// oldClientCompatOptions returns the pre-fix keepalive settings used by the
// historical client implementation.
func oldClientCompatOptions() []Option {
	return []Option{
		WithTimeoutOptions(
			WithKeepalivePing(7*time.Second, 3*time.Second),
		),
	}
}

const (
	// latencyOldPong is the pre-fix static pong timeout used by the old
	// implementation in the latency-focused compatibility tests.
	latencyOldPong = 30 * time.Millisecond

	// latencyOldServerPing is the old server's keepalive ping interval.
	latencyOldServerPing = 50 * time.Millisecond

	// latencyOldClientPing is the old client's keepalive ping interval.
	latencyOldClientPing = 70 * time.Millisecond

	// latencyNewServerPing is the upgraded server's ping interval and also
	// the cap for its dynamic pong timeout.
	latencyNewServerPing = 80 * time.Millisecond

	// latencyNewClientPing is the upgraded client's ping interval and also
	// the cap for its dynamic pong timeout.
	latencyNewClientPing = 100 * time.Millisecond

	// latencyNewSideBasePong is the base pong timeout used by upgraded
	// peers before RTT-based dynamic expansion is applied.
	latencyNewSideBasePong = 50 * time.Millisecond

	// latencyNewSideMultiplier is the RTT multiplier used when computing
	// the upgraded side's dynamic pong timeout.
	latencyNewSideMultiplier = 3

	// latencyStretchedPing is a deliberately long ping interval used for
	// the non-sender so the intended side exclusively drives keepalive.
	latencyStretchedPing = 300 * time.Millisecond

	// latencyCloseTimeout is the outer bound used when waiting for a test
	// connection to close.
	latencyCloseTimeout = 600 * time.Millisecond

	// latencyPingWaitTimeout is the outer bound used when waiting to
	// observe a keepalive ping on the transport.
	latencyPingWaitTimeout = 400 * time.Millisecond

	// latencyOldPongTolerance is the timing slack allowed when asserting
	// that an old-side close happened around the old static pong timeout.
	latencyOldPongTolerance = 25 * time.Millisecond

	// latencyCapTolerance is the timing slack allowed when asserting that
	// an upgraded sender closed around its local ping-cap deadline.
	latencyCapTolerance = 35 * time.Millisecond

	// latencySurvivePastOldPong is how long the upgraded sender must stay
	// alive past the old static timeout window in the success cases.
	latencySurvivePastOldPong = 70 * time.Millisecond

	// latencySimultaneousPing is the shared ping interval used in the
	// simultaneous-keepalive race tests so both peers ping at about the
	// same time.
	latencySimultaneousPing = 120 * time.Millisecond
)

func latencyOldOnlyOpts(ping time.Duration) []Option {
	return []Option{
		WithTimeoutOptions(
			WithKeepalivePing(ping, latencyOldPong),
		),
	}
}

func latencyNewDynamicOpts(ping time.Duration) []Option {
	return []Option{
		WithTimeoutOptions(
			WithKeepalivePing(ping, latencyNewSideBasePong),
			WithDynamicPongTimeout(
				latencyNewSideMultiplier, ping,
			),
		),
	}
}

// requireCloseNearPingTimeout observes a keepalive ping, waits for the local
// connection to close, and asserts that the close happened around the expected
// timeout window for that sender.
func requireCloseNearPingTimeout(t *testing.T, conn *GoBackNConn,
	pingChan <-chan time.Time, pingWaitTimeout, closeTimeout,
	expectedTimeout, tolerance time.Duration) {

	t.Helper()

	pingAt := requirePingSent(t, pingChan, pingWaitTimeout)
	closeAfter := waitForConnClosed(t, conn, closeTimeout)

	require.WithinDuration(
		t, pingAt.Add(expectedTimeout), pingAt.Add(closeAfter), tolerance,
	)
}

// requireSenderSurvivesPastOldTimeout observes the sender's keepalive ping,
// verifies the sender remains connected past the old static timeout window,
// and then checks that normal traffic still succeeds.
func requireSenderSurvivesPastOldTimeout(t *testing.T, sender, receiver *GoBackNConn,
	pingChan <-chan time.Time, payload []byte) {

	t.Helper()

	_ = requirePingSent(t, pingChan, latencyPingWaitTimeout)
	requireConnStaysOpen(t, sender, latencySurvivePastOldPong)
	requireOneWayPayload(t, receiver, sender, payload)
}

// requireOldSideSendsFirstFIN verifies the simultaneous-ping race outcome by
// checking that the old-side peer emits the first FIN after both peers have
// entered the keepalive exchange.
func requireOldSideSendsFirstFIN(t *testing.T, oldConn *GoBackNConn,
	oldPingChan, newPingChan, oldFINChan, newFINChan <-chan time.Time) {

	t.Helper()

	oldPingAt := requirePingSent(t, oldPingChan, latencyPingWaitTimeout)
	_ = requirePingSent(t, newPingChan, latencyPingWaitTimeout)

	select {
	case finAt := <-oldFINChan:
		require.True(t, finAt.After(oldPingAt))

	case <-newFINChan:
		t.Fatalf("expected old-side peer to send FIN first")

	case <-time.After(latencyCloseTimeout):
		t.Fatalf("expected simultaneous keepalive close within %v",
			latencyCloseTimeout)
	}

	requireConnClosed(t, oldConn, latencyCloseTimeout)
}

// TestBackwardsCompatMixedTimeouts ensures that a new client with increased
// ping/pong timeouts and dynamic pong can communicate with an old server using
// the original static timeout values. GBN timeouts are configured independently
// on each side and are not negotiated, so mixed versions should be compatible.
func TestBackwardsCompatMixedTimeouts(t *testing.T) {
	harness := newLatencyHarness(10)

	server, client, cleanup := setUpMixedClientServerConns(
		t, 2, harness, newClientCompatOptions(),
		oldServerCompatOptions(),
	)
	defer cleanup()

	requireOneWayPayload(
		t, client, server, []byte("new client -> old server"),
	)
	requireOneWayPayload(
		t, server, client, []byte("old server -> new client"),
	)

	// Send multiple messages to exercise the RTT tracking that feeds the
	// dynamic pong timeout.
	for i := 0; i < 5; i++ {
		payload := []byte("round trip " + string(rune('0'+i)))

		requireOneWayPayload(t, client, server, payload)
		requireOneWayPayload(t, server, client, payload)
	}
}

// TestBackwardsCompatOldClientNewServer tests the reverse direction: an old
// client with the original timeout values connecting to a new server with
// increased timeouts and dynamic pong.
func TestBackwardsCompatOldClientNewServer(t *testing.T) {
	harness := newLatencyHarness(10)

	server, client, cleanup := setUpMixedClientServerConns(
		t, 2, harness, oldClientCompatOptions(),
		newServerCompatOptions(),
	)
	defer cleanup()

	requireOneWayPayload(
		t, client, server, []byte("old client -> new server"),
	)
	requireOneWayPayload(
		t, server, client, []byte("new server -> old client"),
	)
}

// TestBackwardsCompatLatencyKeepaliveSimultaneousPings tests the concurrent
// keepalive race where both peers send a ping at about the same time.
//
// At a high level, these subtests verify that the peer using the old static
// timeout policy is still the side that initiates closure first.
//
// The test approach is to observe both keepalive pings and then assert that
// the old-side peer sends the first FIN once both sides are in the keepalive
// race.
func TestBackwardsCompatLatencyKeepaliveSimultaneousPings(t *testing.T) {
	t.Run("old server still initiates close first", func(t *testing.T) {
		// Setup: both peers use the same ping interval, so they enter
		// the keepalive race at about the same time.
		// The server uses the old timeout policy, while the client
		// uses upgraded dynamic pong.
		harness := newLatencyHarness(10)
		clientPing := harness.observeClientPing()
		serverPing := harness.observeServerPing()
		clientFIN := harness.observeClientFIN()
		serverFIN := harness.observeServerFIN()

		server, _, cleanup := setUpMixedClientServerConns(
			t, 2, harness,
			latencyNewDynamicOpts(latencySimultaneousPing),
			latencyOldOnlyOpts(latencySimultaneousPing),
		)
		defer cleanup()

		// What this is testing: once both pings are in flight, the old
		// server should still be the side that initiates the shutdown.
		harness.setRTT(80 * time.Millisecond)

		// Verification: after both pings are observed, the server
		// should send the first FIN, and the close should come from
		// the old side's decision. The relevant old-side deadline is
		// latencyOldPong, and it is smaller than the injected RTT.
		requireOldSideSendsFirstFIN(
			t, server, serverPing, clientPing, serverFIN, clientFIN,
		)
	})

	t.Run("old client still initiates close first", func(t *testing.T) {
		// Setup: both peers again use the same ping interval, so they
		// race into keepalive together.
		// This time the client uses the old static timeout policy and
		// the server uses upgraded dynamic pong.
		harness := newLatencyHarness(10)
		clientPing := harness.observeClientPing()
		serverPing := harness.observeServerPing()
		clientFIN := harness.observeClientFIN()
		serverFIN := harness.observeServerFIN()

		_, client, cleanup := setUpMixedClientServerConns(
			t, 2, harness,
			latencyOldOnlyOpts(latencySimultaneousPing),
			latencyNewDynamicOpts(latencySimultaneousPing),
		)
		defer cleanup()

		// What this is testing: the same simultaneous-ping race should
		// hold in the opposite direction, with the old client deciding
		// the close.
		harness.setRTT(80 * time.Millisecond)

		// Verification: after both pings are observed, the client
		// should send the first FIN, and the close should come from
		// the old side's decision. The relevant old-side deadline is
		// latencyOldPong, and it is smaller than the injected RTT.
		requireOldSideSendsFirstFIN(
			t, client, clientPing, serverPing, clientFIN, serverFIN,
		)
	})
}

// TestBackwardsCompatLatencyKeepaliveSenderDriven tests the high-level
// sender-driven keepalive compatibility rules for mixed-version peers under
// injected latency.
//
// At a high level, these subtests verify that the peer sending the keepalive
// ping determines whether the connection fails on the old static schedule,
// survives due to dynamic pong, or fails at the upgraded side's local cap.
//
// The test approach is to create client/server pairs with independent timeout
// options, inject controlled RTT through the latencyHarness, and observe the
// actual keepalive ping on the wire. The non-sender's ping interval is
// stretched so only the intended side drives the keepalive exchange.
func TestBackwardsCompatLatencyKeepaliveSenderDriven(t *testing.T) {
	t.Run("old server sender still times out first", func(t *testing.T) {
		// Setup: the old server is the only keepalive sender.
		// The upgraded client gets a stretched ping interval so it only
		// answers. RTT is above the old static pong timeout.
		harness := newLatencyHarness(10)
		serverPing := harness.observeServerPing()

		server, _, cleanup := setUpMixedClientServerConns(
			t, 2, harness,
			latencyNewDynamicOpts(latencyStretchedPing),
			latencyOldOnlyOpts(latencyOldServerPing),
		)
		defer cleanup()

		// What this is testing: even with an upgraded peer on the other
		// side, the old server's local timeout policy should still
		// decide when the connection is torn down.
		harness.setRTT(80 * time.Millisecond)

		// Verification: after the observed ping, the server should
		// close on the old pong timeout schedule.
		requireCloseNearPingTimeout(
			t, server, serverPing, latencyPingWaitTimeout,
			latencyCloseTimeout, latencyOldPong,
			latencyOldPongTolerance,
		)
	})

	t.Run("old client sender still times out first", func(t *testing.T) {
		// Setup: the old client is the only keepalive sender.
		// The upgraded server gets a stretched ping interval so it only
		// answers. The injected RTT is again above the old static pong
		// timeout.
		harness := newLatencyHarness(10)
		clientPing := harness.observeClientPing()

		_, client, cleanup := setUpMixedClientServerConns(
			t, 2, harness, latencyOldOnlyOpts(latencyOldClientPing),
			latencyNewDynamicOpts(latencyStretchedPing),
		)
		defer cleanup()

		// What this is testing: the same sender-driven failure mode
		// hold in the opposite direction, with the old client deciding
		// the close.
		harness.setRTT(80 * time.Millisecond)

		// Verification: after the observed ping, the client should
		// close on the old pong timeout schedule.
		requireCloseNearPingTimeout(
			t, client, clientPing, latencyPingWaitTimeout,
			latencyCloseTimeout, latencyOldPong,
			latencyOldPongTolerance,
		)
	})

	t.Run(
		"new client sender absorbs latency below cap",
		func(t *testing.T) {
			// Setup: the upgraded client is the keepalive sender.
			// The old server is stretched so it only answers.
			// The client's RTT estimate is seeded so dynamic pong
			// active before the exchange.
			harness := newLatencyHarness(10)
			clientPing := harness.observeClientPing()

			server, client, cleanup := setUpMixedClientServerConns(
				t, 2, harness,
				latencyNewDynamicOpts(latencyNewClientPing),
				latencyOldOnlyOpts(latencyStretchedPing),
			)
			defer cleanup()

			setSmoothedRTT(client, 60*time.Millisecond)
			harness.setRTT(60 * time.Millisecond)

			// What this is testing: RTT above the old timeout,
			// but still below the upgraded sender's cap, should be
			// tolerated by the new client.
			//
			// Verification: after the observed ping, the client
			// should stay alive past the old timeout window.
			// Normal traffic should still succeed afterward.
			requireSenderSurvivesPastOldTimeout(
				t, client, server, clientPing,
				[]byte("old server -> new client"),
			)
		},
	)

	t.Run(
		"new server sender absorbs latency below cap",
		func(t *testing.T) {
			// Setup: the upgraded server is the keepalive sender.
			// The old client is stretched so it only answers.
			// The server's RTT estimate is seeded so dynamic pong
			// active before the exchange.
			harness := newLatencyHarness(10)
			serverPing := harness.observeServerPing()

			server, client, cleanup := setUpMixedClientServerConns(
				t, 2, harness,
				latencyOldOnlyOpts(latencyStretchedPing),
				latencyNewDynamicOpts(latencyNewServerPing),
			)
			defer cleanup()

			setSmoothedRTT(server, 60*time.Millisecond)
			harness.setRTT(60 * time.Millisecond)

			// What this is testing: the same upgraded-sender
			// success case in the opposite direction.
			//
			// Verification: after the observed ping, the server
			// should stay alive past the old timeout window.
			// Normal traffic should still succeed afterward.
			requireSenderSurvivesPastOldTimeout(
				t, server, client, serverPing,
				[]byte("old client -> new server"),
			)
		},
	)

	t.Run("new client sender fails above ping cap", func(t *testing.T) {
		// Setup: the upgraded client is the only keepalive sender.
		// The old server is stretched, and the client's RTT estimate is
		// seeded so dynamic pong would want to wait longer than the
		// client's local cap.
		harness := newLatencyHarness(10)
		clientPing := harness.observeClientPing()

		_, client, cleanup := setUpMixedClientServerConns(
			t, 2, harness,
			latencyNewDynamicOpts(latencyNewClientPing),
			latencyOldOnlyOpts(latencyStretchedPing),
		)
		defer cleanup()

		setSmoothedRTT(client, 220*time.Millisecond)
		harness.setRTT(220 * time.Millisecond)

		// What this is testing: dynamic pong does not let the upgraded
		// sender wait indefinitely; it is still bounded by the sender's
		// own ping interval.
		//
		// Verification: after the observed ping, the client should
		// close on the client ping interval rather than surviving for
		// the full RTT.
		requireCloseNearPingTimeout(
			t, client, clientPing, latencyPingWaitTimeout,
			latencyCloseTimeout, latencyNewClientPing,
			latencyCapTolerance,
		)
	})

	t.Run("new server sender fails above ping cap", func(t *testing.T) {
		// Setup: the upgraded server is the only keepalive sender.
		// The old client is stretched, and the server's RTT estimate is
		// seeded so dynamic pong would want to wait longer than the
		// server's local cap.
		harness := newLatencyHarness(10)
		serverPing := harness.observeServerPing()

		server, _, cleanup := setUpMixedClientServerConns(
			t, 2, harness, latencyOldOnlyOpts(latencyStretchedPing),
			latencyNewDynamicOpts(latencyNewServerPing),
		)
		defer cleanup()

		setSmoothedRTT(server, 180*time.Millisecond)
		harness.setRTT(180 * time.Millisecond)

		// What this is testing: the upgraded server has the same
		// cap-limited behavior as the upgraded client in the previous
		// subtest.
		//
		// Verification: after the observed ping, the server should
		// close on the server ping interval rather than surviving for
		// the full RTT.
		requireCloseNearPingTimeout(
			t, server, serverPing, latencyPingWaitTimeout,
			latencyCloseTimeout, latencyNewServerPing,
			latencyCapTolerance,
		)
	})
}

// TestBackwardsCompatLatencyKeepaliveAllUpgraded tests the same sender-driven
// scenarios as the mixed-version suite, but with upgraded dynamic pong
// behavior on both peers.
func TestBackwardsCompatLatencyKeepaliveAllUpgraded(t *testing.T) {
	t.Run("client sender absorbs latency below cap", func(t *testing.T) {
		// Setup: the upgraded client is the keepalive sender.
		// The upgraded server is stretched so it only answers.
		// The client's RTT estimate is seeded so dynamic pong is on.
		harness := newLatencyHarness(10)
		clientPing := harness.observeClientPing()

		server, client, cleanup := setUpMixedClientServerConns(
			t, 2, harness,
			latencyNewDynamicOpts(latencyNewClientPing),
			latencyNewDynamicOpts(latencyStretchedPing),
		)
		defer cleanup()

		setSmoothedRTT(client, 60*time.Millisecond)
		harness.setRTT(60 * time.Millisecond)

		// What this is testing: with both peers upgraded, RTT below the
		// sender's cap should still be tolerated.
		//
		// Verification: after the observed ping, the client should stay
		// stay alive past the old timeout window.
		// Normal traffic should still succeed afterward.
		requireSenderSurvivesPastOldTimeout(
			t, client, server, clientPing,
			[]byte("upgraded server -> upgraded client"),
		)
	})

	t.Run("server sender absorbs latency below cap", func(t *testing.T) {
		// Setup: the upgraded server is the keepalive sender.
		// The upgraded client is stretched so it only answers.
		// The server's RTT estimate is seeded so dynamic pong is on.
		harness := newLatencyHarness(10)
		serverPing := harness.observeServerPing()

		server, client, cleanup := setUpMixedClientServerConns(
			t, 2, harness,
			latencyNewDynamicOpts(latencyStretchedPing),
			latencyNewDynamicOpts(latencyNewServerPing),
		)
		defer cleanup()

		setSmoothedRTT(server, 60*time.Millisecond)
		harness.setRTT(60 * time.Millisecond)

		// What this is testing: the same all-upgraded success case
		// in the opposite direction.
		//
		// Verification: after the observed ping, the server should stay
		// stay alive past the old timeout window.
		// Normal traffic should still succeed afterward.
		requireSenderSurvivesPastOldTimeout(
			t, server, client, serverPing,
			[]byte("upgraded client -> upgraded server"),
		)
	})

	t.Run("client sender still fails above ping cap", func(t *testing.T) {
		// Setup: the upgraded client is the keepalive sender.
		// The upgraded server is stretched.
		// The client's RTT estimate is seeded so dynamic pong would
		// to wait past the local cap.
		harness := newLatencyHarness(10)
		clientPing := harness.observeClientPing()

		_, client, cleanup := setUpMixedClientServerConns(
			t, 2, harness,
			latencyNewDynamicOpts(latencyNewClientPing),
			latencyNewDynamicOpts(latencyStretchedPing),
		)
		defer cleanup()

		setSmoothedRTT(client, 220*time.Millisecond)
		harness.setRTT(220 * time.Millisecond)

		// What this is testing: even with both peers upgraded,
		// the sender is still bounded by its own ping cap.
		//
		// Verification: after the observed ping, the client should
		// close on the client ping interval rather than surviving
		// for the RTT.
		requireCloseNearPingTimeout(
			t, client, clientPing, latencyPingWaitTimeout,
			latencyCloseTimeout, latencyNewClientPing,
			latencyCapTolerance,
		)
	})

	t.Run("server sender still fails above ping cap", func(t *testing.T) {
		// Setup: the upgraded server is the keepalive sender.
		// The upgraded client is stretched.
		// The server's RTT estimate is seeded so dynamic pong would
		// to wait past the local cap.
		harness := newLatencyHarness(10)
		serverPing := harness.observeServerPing()

		server, _, cleanup := setUpMixedClientServerConns(
			t, 2, harness,
			latencyNewDynamicOpts(latencyStretchedPing),
			latencyNewDynamicOpts(latencyNewServerPing),
		)
		defer cleanup()

		setSmoothedRTT(server, 180*time.Millisecond)
		harness.setRTT(180 * time.Millisecond)

		// What this is testing: the same cap-limited behavior should
		// hold when the upgraded server is the sender.
		//
		// Verification: after the observed ping, the server should
		// close on the server ping interval rather than surviving for
		// the RTT.
		requireCloseNearPingTimeout(
			t, server, serverPing, latencyPingWaitTimeout,
			latencyCloseTimeout, latencyNewServerPing,
			latencyCapTolerance,
		)
	})
}

func setUpClientServerConns(t *testing.T, n uint8,
	cRead, sRead func(ctx context.Context) ([]byte, error),
	cWrite, sWrite func(ctx context.Context, b []byte) error,
	opts ...Option) (*GoBackNConn, *GoBackNConn, func()) {

	var (
		server *GoBackNConn
		err    error
		wg     sync.WaitGroup
	)

	ctx := context.Background()

	wg.Add(1)
	go func() {
		defer wg.Done()

		var err error
		server, err = NewServerConn(ctx, sWrite, sRead, opts...)
		require.NoError(t, err)
	}()

	// Give the server time to be ready for the handshake
	time.Sleep(time.Millisecond * 200)

	client, err := NewClientConn(ctx, n, cWrite, cRead, opts...)
	require.NoError(t, err)

	wg.Wait()

	return server, client, func() {
		client.Close()
		server.Close()
	}
}
