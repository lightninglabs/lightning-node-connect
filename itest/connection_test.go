package itest

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/lightninglabs/terminal-connect/itest/mockrpc"
	"github.com/stretchr/testify/require"
)

// testHappyPath ensures that client and server are able to communicate
// as expected in the case where no connections are dropped.
func testHappyPath(t *harnessTest) {
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		resp, err := t.client.clientConn.MockServiceMethod(
			ctx, &mockrpc.Request{},
		)
		require.NoError(t.t, err)
		require.Equal(t.t, defaultServerResponse, string(resp.Resp))
		t.t.Logf("Done with one call")
	}
}

// testHashmailServerReconnect tests that client and server are able to
// continue with their communication after the hashmail server restarts.
func testHashmailServerReconnect(t *harnessTest) {
	ctx := context.Background()

	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, defaultServerResponse, string(resp.Resp))
	t.t.Logf("Done with initial call")

	// Shut down hashmail server
	require.NoError(t.t, t.hmserver.stop())
	t.t.Logf("")

	time.Sleep(5000 * time.Millisecond)

	// Restart hashmail server
	require.NoError(t.t, t.hmserver.start())
	t.t.Logf("Done with hashmail server re-init")

	time.Sleep(5000 * time.Millisecond)

	resp, err = t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, defaultServerResponse, string(resp.Resp))
	t.t.Logf("Done with second call")
}

func testClientReconnect(t *harnessTest) {
	ctx := context.Background()

	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, defaultServerResponse, string(resp.Resp))
	t.t.Logf("Done with initial call")

	require.NoError(t.t, t.client.cleanup())
	t.t.Logf("Done with client cleanup")

	time.Sleep(5000 * time.Millisecond)

	require.NoError(t.t, t.client.setConn(t.server.password[:]))
	t.t.Logf("Done with client re-init")

	time.Sleep(5000 * time.Millisecond)

	resp, err = t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, defaultServerResponse, string(resp.Resp))
	t.t.Logf("Done with second call")
}

func testLargeResponse(t *harnessTest) {
	ctx := context.Background()

	largeResp := make([]byte, 1024*1024*100)
	rand.Read(largeResp)
	t.server.server.SetResponse(largeResp)

	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, largeResp, resp.Resp)
}
