package itest

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-errors/errors"
	"github.com/lightninglabs/aperture"
	"github.com/lightninglabs/terminal-connect/gbn"
	"github.com/lightninglabs/terminal-connect/mailbox"
	"github.com/lightningnetwork/lnd"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"
	"github.com/stretchr/testify/require"
)

// testCase is a struct that holds a single test case.
type testCase struct {
	name string
	test func(t *harnessTest)
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

	hmserver *hashmailHarness
}

// newHarnessTest creates a new instance of a harnessTest from a regular
// testing.T instance.
func newHarnessTest(t *testing.T, client *clientHarness, server *serverHarness,
	hashmail *hashmailHarness) *harnessTest {

	return &harnessTest{t, nil, client, server, hashmail}
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
func (h *harnessTest) setupLogging() {
	logWriter := build.NewRotatingLogWriter()
	interceptor, err := signal.Intercept()
	require.NoError(h.t, err)

	aperture.SetupLoggers(logWriter, interceptor)
	lnd.AddSubLogger(logWriter, mailbox.Subsystem, interceptor, mailbox.UseLogger)
	lnd.AddSubLogger(logWriter, gbn.Subsystem, interceptor, gbn.UseLogger)

	err = build.ParseAndSetDebugLevels("debug,PRXY=warn", logWriter)
	require.NoError(h.t, err)
}

// shutdown stops the client, server and hashmail server
func (h *harnessTest) shutdown() error {
	var returnErr error

	err := h.hmserver.stop()
	if err != nil {
		returnErr = err
	}

	err = h.client.cleanup()
	if err != nil {
		returnErr = err
	}

	err = h.server.stop()
	if err != nil {
		returnErr = err
	}

	return returnErr
}

// setupHarnesses creates new server, client and hashmail harnesses.
func setupHarnesses(t *testing.T) (*clientHarness, *serverHarness,
	*hashmailHarness) {

	hashmailHarness := newHashmailHarness()
	if err := hashmailHarness.start(); err != nil {
		t.Fatalf("could not start hashmail server: %v", err)
	}

	serverHarness := newServerHarness(hashmailHarness.apertureCfg.ListenAddr)
	if err := serverHarness.start(); err != nil {
		t.Fatalf("could not start server: %v", err)
	}

	select {
	case err := <-serverHarness.errChan:
		t.Fatalf("could not start server: %v", err)
	default:
	}

	// Give the server some time to set up the first mailbox
	time.Sleep(1000 * time.Millisecond)

	clientHarness := newClientHarness(hashmailHarness.apertureCfg.ListenAddr)
	if err := clientHarness.setConn(serverHarness.password[:]); err != nil {
		t.Fatalf("could not connect client: %v", err)
	}

	return clientHarness, serverHarness, hashmailHarness
}
