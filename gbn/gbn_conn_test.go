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

func TestQueueSize(t *testing.T) {
	gbn := &GoBackNConn{
		s:           4,
		sendSeqBase: 0,
		sendSeqTop:  0,
	}

	require.Equal(t, uint8(0), gbn.queueSize())

	gbn.sendSeqBase = 2
	gbn.sendSeqTop = 3
	require.Equal(t, uint8(1), gbn.queueSize())

	gbn.sendSeqBase = 3
	gbn.sendSeqTop = 2
	require.Equal(t, uint8(3), gbn.queueSize())
}

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

	server, client, cleanup, err := setUpClientServerConns(t, 2, s1Read, s2Read, s2Write, s1Write)
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

	count := 0
	s1Write := func(ctx context.Context, b []byte) error {
		count++

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

	p1, p2, close, err := setUpClientServerConns(t, 2, s1Read, s2Read, s2Write, s1Write)
	defer close()

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
		count uint8
		n     uint8 = 2
	)
	s2Write := func(ctx context.Context, b []byte) error {
		defer func() {
			count++
		}()

		// Drop the first n messages (after handshake)
		if count > 2 && count < n+2 {
			return nil
		}

		select {
		case s2Chan <- b:
			return nil
		case <-ctx.Done():
		}
		return nil
	}

	p1, p2, close, err := setUpClientServerConns(t, n, s1Read, s2Read, s2Write, s1Write)
	defer close()

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
	count := 0
	s1Write := func(ctx context.Context, b []byte) error {
		defer func() {
			count++
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

	p1, p2, close, err := setUpClientServerConns(t, 2, s1Read, s2Read, s2Write, s1Write)
	defer close()

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
	count := 0
	s1Write := func(ctx context.Context, b []byte) error {
		defer func() {
			count++
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
	count2 := 0
	s2Write := func(ctx context.Context, b []byte) error {
		defer func() {
			count2++
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

	p1, p2, close, err := setUpClientServerConns(t, 2, s1Read, s2Read, s2Write, s1Write)
	defer close()

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

	p1, p2, close, err := setUpClientServerConns(t, 2, s1Read, s2Read, s2Write, s1Write)
	defer close()

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

	p1, p2, close, err := setUpClientServerConns(t, 2, s1Read, s2Read, s2Write, s1Write)
	defer close()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")

	err = p1.Send(payload1)
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
		n     uint8 = 3
		count uint8 = 0
	)
	s1Write := func(ctx context.Context, b []byte) error {
		defer func() {
			count++
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

	p1, p2, close, err := setUpClientServerConns(t, n, s1Read, s2Read, s2Write, s1Write)
	defer close()

	payload1 := []byte("payload 1")
	payload2 := []byte("payload 2")
	payload3 := []byte("payload 3")

	err = p1.Send(payload1)
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

	p1, p2, close, err := setUpClientServerConns(t, 2, s1Read, s2Read, s2Write, s1Write)
	defer close()

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
	go func() {
		wg.Add(1)
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
	err = p1.Send(payload1)
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
	count := 0
	s1Write := func(ctx context.Context, b []byte) error {
		defer func() {
			count++
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

	p1, p2, close, err := setUpClientServerConns(t, 2, s1Read, s2Read, s2Write, s1Write)
	defer close()

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

	err = p2.Send(payload3)
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

	p1, p2, cleanup, err := setUpClientServerConns(t, 100, s1Read, s2Read, s2Write, s1Write)
	require.NoError(t, err)
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

	p1, p2, cleanup, err := setUpClientServerConns(t, 100, s1Read, s2Read, s2Write, s1Write)
	require.NoError(t, err)
	defer cleanup()

	payload1 := []byte("payload 1")

	err = p1.Send(payload1)
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

	server, client, cleanup, err := setUpClientServerConns(
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

func setUpClientServerConns(t *testing.T, n uint8,
	cRead, sRead func(ctx context.Context) ([]byte, error),
	cWrite, sWrite func(ctx context.Context, b []byte) error,
	opts ...Option) (*GoBackNConn,
	*GoBackNConn, func(), error) {

	var (
		server *GoBackNConn
		err    error
		wg     sync.WaitGroup
	)

	ctx := context.Background()

	wg.Add(1)
	go func() {
		defer wg.Done()
		server, err = NewServerConn(ctx, sWrite, sRead, opts...)
		require.NoError(t, err)
	}()

	// Give the server time to be ready for the handshake
	time.Sleep(time.Millisecond * 200)

	client, err := NewClientConn(n, cWrite, cRead, opts...)
	require.NoError(t, err)

	wg.Wait()

	return server, client, func() {
		client.Close()
		server.Close()
	}, nil
}
