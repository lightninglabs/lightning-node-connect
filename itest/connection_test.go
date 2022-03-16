package itest

import (
	"context"
	"crypto/rand"

	"github.com/lightninglabs/lightning-node-connect/itest/mockrpc"
	"github.com/stretchr/testify/require"
)

var (
	defaultMessage = []byte("some default message")
)

// testHappyPath ensures that client and server are able to communicate
// as expected in the case where no connections are dropped.
func testHappyPath(t *harnessTest) {
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		resp, err := t.client.clientConn.MockServiceMethod(
			ctx, &mockrpc.Request{Req: defaultMessage},
		)
		require.NoError(t.t, err)
		require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))
	}
}

// testHashmailServerReconnect tests that client and server are able to
// continue with their communication after the hashmail server restarts.
func testHashmailServerReconnect(t *harnessTest) {
	ctx := context.Background()

	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))

	// Shut down hashmail server
	require.NoError(t.t, t.hmserver.stop())
	t.t.Logf("")

	// Restart hashmail server
	require.NoError(t.t, t.hmserver.start())

	resp, err = t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))
}

func testClientReconnect(t *harnessTest) {
	ctx := context.Background()

	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))

	// Stop the client.
	require.NoError(t.t, t.client.cleanup())

	// Restart the client.
	require.NoError(t.t, t.client.start())

	resp, err = t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))
}

func testServerReconnect(t *harnessTest) {
	ctx := context.Background()

	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))

	t.server.stop()
	require.NoError(t.t, t.server.start())

	select {
	case err := <-t.server.errChan:
		if err != nil {
			t.Fatalf("could not start server: %v", err)
		}
	default:
	}

	// To replicate how the browser's behaviour, we retry this call a few
	// times if an error is received.
	for i := 0; i <= 3; i++ {
		resp, err = t.client.clientConn.MockServiceMethod(
			ctx, &mockrpc.Request{Req: defaultMessage},
		)
		if err == nil {
			break
		}
	}
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))
}

func testLargeResponse(t *harnessTest) {
	ctx := context.Background()

	req := make([]byte, 0.5*1024*1024) // a 0.5MB req will return a 5MB resp
	_, err := rand.Read(req)
	require.NoError(t.t, err)

	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: req},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(req)*10, len(resp.Resp))
}
