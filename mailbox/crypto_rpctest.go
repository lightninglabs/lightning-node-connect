//go:build rpctest
// +build rpctest

package mailbox

import "github.com/btcsuite/btcwallet/waddrmgr"

func init() {
	// For the purposes of our itest and unit tests, we'll crank down the
	// scrypt params a bit.
	scryptN = waddrmgr.FastScryptOptions.N
	scryptR = waddrmgr.FastScryptOptions.R
	scryptP = waddrmgr.FastScryptOptions.P
}
