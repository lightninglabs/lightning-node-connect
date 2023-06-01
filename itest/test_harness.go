package itest

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-errors/errors"
	"github.com/lightninglabs/aperture"
	"github.com/lightninglabs/lightning-node-connect/gbn"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"
	"github.com/stretchr/testify/require"
)

var interceptor *signal.Interceptor

const testnetMailbox = "mailbox.testnet.lightningcluster.com:443"

// testCase is a struct that holds a single test case.
type testCase struct {
	name string
	test func(t *harnessTest)

	// localMailboxOnly indicates if the test should only be run against a
	// local mailbox instance and not against the staging mailbox.
	localMailboxOnly bool
}

// harnessTest wraps a regular testing.T providing enhanced error detection
// and propagation. All error will be augmented with a full stack-trace in
// order to aid in debugging. Additionally, any panics caused by active
// test cases will also be handled and represented as fatals.
type harnessTest struct {
	t *testing.T

	// testCase is populated during test execution and represents the
	// current test case.
	testCase *testCase

	client *clientHarness

	server *serverHarness

	hmserver *HashmailHarness
}

// testConfig determines the way in which the test will be set up.
type testConfig struct {
	stagingMailbox bool
	grpcClientConn bool
}

// newHarnessTest creates a new instance of a harnessTest from a regular
// testing.T instance.
func newHarnessTest(t *testing.T, cfg *testConfig) *harnessTest {
	ht := &harnessTest{t: t}

	mailboxAddr := testnetMailbox
	var insecure bool

	if !cfg.stagingMailbox {
		ht.hmserver = NewHashmailHarness()

		if err := ht.hmserver.Start(); err != nil {
			t.Fatalf("could not start hashmail server: %v", err)
		}

		mailboxAddr = ht.hmserver.ApertureCfg.ListenAddr
		insecure = true
	}

	var err error
	ht.server, err = newServerHarness(mailboxAddr, insecure)
	require.NoError(t, err)
	require.NoError(t, ht.server.start())
	t.Cleanup(func() {
		ht.server.stop()
	})

	select {
	case err := <-ht.server.errChan:
		if err != nil {
			t.Fatalf("could not start server: %v", err)
		}
	default:
	}

	// Give the server some time to set up the first mailbox
	time.Sleep(1000 * time.Millisecond)

	ht.client, err = newClientHarness(
		mailboxAddr, ht.server.passphraseEntropy, insecure,
		cfg.grpcClientConn,
	)
	require.NoError(t, err)

	require.NoError(t, ht.client.start())
	t.Cleanup(func() {
		_ = ht.client.cleanup()
	})

	return ht
}

// Skipf calls the underlying testing.T's Skip method, causing the current test
// to be skipped.
func (h *harnessTest) Skipf(format string, args ...interface{}) {
	h.t.Skipf(format, args...)
}

// Fatalf causes the current active test case to fail with a fatal error. All
// integration tests should mark test failures solely with this method due to
// the error stack traces it produces.
func (h *harnessTest) Fatalf(format string, a ...interface{}) {
	stacktrace := errors.Wrap(fmt.Sprintf(format, a...), 1).ErrorStack()

	if h.testCase != nil {
		h.t.Fatalf("Failed: (%v): exited with error: \n"+
			"%v", h.testCase.name, stacktrace)
	} else {
		h.t.Fatalf("Error outside of test: %v", stacktrace)
	}
}

// RunTestCase executes a harness test case. Any errors or panics will be
// represented as fatal.
func (h *harnessTest) RunTestCase(testCase *testCase) {
	h.testCase = testCase
	defer func() {
		h.testCase = nil
	}()

	defer func() {
		if err := recover(); err != nil {
			description := errors.Wrap(err, 2).ErrorStack()
			h.t.Fatalf("Failed: (%v) panicked with: \n%v",
				h.testCase.name, description)
		}
	}()

	testCase.test(h)
}

func (h *harnessTest) Logf(format string, args ...interface{}) {
	h.t.Logf(format, args...)
}

func (h *harnessTest) Log(args ...interface{}) {
	h.t.Log(args...)
}

// setupLogging initializes the logging subsystem for the server and client
// packages.
func setupLogging(t *testing.T) {
	logWriter := build.NewRotatingLogWriter()

	if interceptor != nil {
		return
	}

	ic, err := signal.Intercept()
	require.NoError(t, err)
	interceptor = &ic

	aperture.SetupLoggers(logWriter, *interceptor)
	lnd.AddSubLogger(
		logWriter, mailbox.Subsystem, *interceptor, mailbox.UseLogger,
	)
	lnd.AddSubLogger(logWriter, gbn.Subsystem, *interceptor, gbn.UseLogger)

	err = build.ParseAndSetDebugLevels(
		"debug,PRXY=warn,GOBN=trace", logWriter,
	)
	require.NoError(t, err)
}

// shutdown stops the client, server and hashmail server
func (h *harnessTest) shutdown() error {
	var returnErr error

	err := h.client.cleanup()
	if err != nil {
		returnErr = err
	}

	h.server.stop()

	if h.hmserver != nil {
		err := h.hmserver.Stop()
		if err != nil {
			returnErr = err
		}
	}

	return returnErr
}
