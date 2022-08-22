package itest

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/lightninglabs/lightning-node-connect/itest/mockrpc"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/stretchr/testify/require"
)

var (
	defaultMessage = []byte("some default message")
)

const defaultTimeout = 30 * time.Second

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
	// Assert that the server and client are connected.
	assertServerStatus(t, mailbox.ServerStatusInUse)
	assertClientStatus(t, mailbox.ClientStatusConnected)

	ctx := context.Background()
	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))

	// Shut down hashmail server
	require.NoError(t.t, t.hmserver.stop())
	t.t.Logf("")

	// Check that the client and server status are updated appropriately.
	assertServerStatus(t, mailbox.ServerStatusNotConnected)
	assertClientStatus(t, mailbox.ClientStatusNotConnected)

	// Restart hashmail server
	require.NoError(t.t, t.hmserver.start())

	// Check that the client and server successfully reconnect.
	assertServerStatus(t, mailbox.ServerStatusInUse)
	assertClientStatus(t, mailbox.ClientStatusConnected)

	resp, err = t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))
}

// testClientReconnect tests that the client and server are able to reestablish
// their connection if the client disconnects and reconnects.
func testClientReconnect(t *harnessTest) {
	// Assert that the server and client are connected.
	assertServerStatus(t, mailbox.ServerStatusInUse)

	ctx := context.Background()
	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))

	// Stop the client.
	require.NoError(t.t, t.client.cleanup())

	// Check that the server status is updated appropriately.
	assertServerStatus(t, mailbox.ServerStatusIdle)

	// Restart the client.
	require.NoError(t.t, t.client.start())

	// Check that the client and server successfully reconnect.
	assertServerStatus(t, mailbox.ServerStatusInUse)

	resp, err = t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))
}

// testServerReconnect tests that the client and server are able to reestablish
// their connection if the server disconnects and reconnects.
func testServerReconnect(t *harnessTest) {
	// Assert that the server and client are connected.
	assertClientStatus(t, mailbox.ClientStatusConnected)

	ctx := context.Background()
	resp, err := t.client.clientConn.MockServiceMethod(
		ctx, &mockrpc.Request{Req: defaultMessage},
	)
	require.NoError(t.t, err)
	require.Equal(t.t, len(defaultMessage)*10, len(resp.Resp))

	t.server.stop()

	// Assert that the client status is updated appropriately.
	assertClientStatus(t, mailbox.ClientStatusSessionNotFound)

	require.NoError(t.t, t.server.start())

	select {
	case err := <-t.server.errChan:
		if err != nil {
			t.Fatalf("could not start server: %v", err)
		}

	case <-time.After(defaultTimeout):
		t.Fatalf("could not start server in time")
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

	// Assert that the client's status has been correctly updated.
	assertClientStatus(t, mailbox.ClientStatusConnected)
}

// testLargeResponse tests that the client and server can successfully send
// a large amount of data back and forth reliably.
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

func assertServerStatus(t *harnessTest, status mailbox.ServerStatus) {
	err := wait.Predicate(func() bool {
		t.server.statusMu.Lock()
		defer t.server.statusMu.Unlock()

		return t.server.status == status
	}, defaultTimeout)
	require.NoError(t.t, err)
}

func assertClientStatus(t *harnessTest, status mailbox.ClientStatus) {
	err := wait.Predicate(func() bool {
		return t.client.client.ConnStatus() == status
	}, defaultTimeout)
	require.NoError(t.t, err)
}
