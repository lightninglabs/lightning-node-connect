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
		name: "test client reconnect",
		test: testClientReconnect,
	},
}
