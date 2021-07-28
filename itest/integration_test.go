package itest

import (
	"testing"
)

func TestTerminalConnect(t *testing.T) {
	// If no tests are registered, then we can exit early.
	if len(testCases) == 0 {
		t.Skip("integration tests not selected")
	}

	ht := newHarnessTest(t, nil, nil, nil)
	ht.setupLogging()

	t.Logf("Running %v integration tests", len(testCases))
	for _, testCase := range testCases {

		success := t.Run(testCase.name, func(t1 *testing.T) {
			clientHarness, serverHarness, hashmailHarness := setupHarnesses(t1)

			ht := newHarnessTest(
				t1, clientHarness, serverHarness,
				hashmailHarness,
			)

			// Now we have everything to run the test case.
			ht.RunTestCase(testCase)

			// Shut down both client, server and hashmail server
			// to remove all state.
			err := ht.shutdown()
			if err != nil {
				t1.Fatalf("error shutting down harness: %v", err)
			}
		})

		// Close at the first failure. Mimic behavior of original test
		// framework.
		if !success {
			break
		}
	}
}
