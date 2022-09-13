package itest

import (
	"fmt"
	"testing"
)

// TestLightningNodeConnect runs the itests against a local mailbox instance.
func TestLightningNodeConnect(t *testing.T) {
	// If no tests are registered, then we can exit early.
	if len(testCases) == 0 {
		t.Skip("integration tests not selected")
	}

	setupLogging(t)

	testConfigs := []*testConfig{
		{stagingMailbox: false, grpcClientConn: false},
		{stagingMailbox: false, grpcClientConn: true},
		{stagingMailbox: true, grpcClientConn: false},
		{stagingMailbox: true, grpcClientConn: true},
	}

	t.Logf("Running %v integration tests", len(testCases))
	for _, testCase := range testCases {

		testCase := testCase
		for _, config := range testConfigs {
			config := config

			if config.stagingMailbox && testCase.localMailboxOnly {
				continue
			}

			name := fmt.Sprintf("%s(stagingMailbox:%t,"+
				"grpcClientConn:%t)", testCase.name,
				config.stagingMailbox, config.grpcClientConn)

			success := t.Run(name, func(t1 *testing.T) {
				ht := newHarnessTest(t1, config)

				// Now we have everything to run the test case.
				ht.RunTestCase(testCase)

				// Shut down both client, server and hashmail
				// server to remove all state.
				err := ht.shutdown()
				if err != nil {
					t1.Fatalf("error shutting down "+
						"harness: %v", err)
				}
			})

			// Close at the first failure. Mimic behavior of
			// original test framework.
			if !success {
				break
			}
		}
	}
}
