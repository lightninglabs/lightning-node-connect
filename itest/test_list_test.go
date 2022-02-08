package itest

var testCases = []*testCase{
	{
		name: "test happy path",
		test: testHappyPath,
	},
	{
		name: "test hashmail server reconnect",
		test: testHashmailServerReconnect,
	},
	{
		name: "test large response",
		test: testLargeResponse,
	},
	{
		name: "test client reconnect",
		test: testClientReconnect,
	},
	{
		name: "test server reconnect",
		test: testServerReconnect,
	},
}

var stagingMailboxTests = []*testCase{
	{
		name: "test happy path",
		test: testHappyPath,
	},
	{
		name: "test client reconnect",
		test: testClientReconnect,
	},
	{
		name: "test large response",
		test: testLargeResponse,
	},
	{
		name: "test server reconnect",
		test: testServerReconnect,
	},
}
