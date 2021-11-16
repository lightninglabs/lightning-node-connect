//go:build rpctest
// +build rpctest

package mailbox

func init() {
	// For the purposes of our itest and unit tests, we'll crank down the
	// scrypt params a bit.
	scryptN = 16
	scryptR = 8
	scryptP = 1
}
