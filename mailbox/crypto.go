package mailbox

import (
	"crypto/rand"

	"github.com/kkdai/bstream"
	"github.com/lightningnetwork/lnd/aezeed"
)

const (
	ClientPointPreimage = "TerminalConnectClient"
	ServerPointPreimage = "TerminalConnectServer"
	NumPasswordWords    = 8 // TODO: make shorter by masking leftover bits.
	NumPasswordBytes    = (NumPasswordWords * aezeed.BitsPerWord) / 8
)

// NewPassword generates a new one-time-use password, represented as a set of
// mnemonic words and the raw entropy itself.
func NewPassword() ([NumPasswordWords]string, [NumPasswordBytes]byte, error) {
	var (
		passwordEntropy [NumPasswordBytes]byte
		password        [NumPasswordWords]string
		err             error
	)
	if _, err = rand.Read(passwordEntropy[:]); err != nil {
		return password, passwordEntropy, err
	}

	password, err = PasswordEntropyToMnemonic(passwordEntropy)
	if err != nil {
		return password, passwordEntropy, err
	}

	return password, passwordEntropy, nil
}

func PasswordEntropyToMnemonic(
	entropy [NumPasswordBytes]byte) ([NumPasswordWords]string, error) {

	var (
		password   [NumPasswordWords]string
		cipherBits = bstream.NewBStreamReader(entropy[:])
	)
	for i := 0; i < NumPasswordWords; i++ {
		index, err := cipherBits.ReadBits(aezeed.BitsPerWord)
		if err != nil {
			return password, err
		}

		password[i] = aezeed.DefaultWordList[index]
	}

	return password, nil
}

// PasswordMnemonicToEntropy reverses the mnemonic word encoding and returns the
// raw password entropy bytes.
func PasswordMnemonicToEntropy(
	password [NumPasswordWords]string) [NumPasswordBytes]byte {

	var passwordEntropy [NumPasswordBytes]byte
	cipherBits := bstream.NewBStreamWriter(NumPasswordBytes)
	for _, word := range password {
		index := uint64(aezeed.ReverseWordMap[word])
		cipherBits.WriteBits(index, aezeed.BitsPerWord)
	}

	copy(passwordEntropy[:], cipherBits.Bytes())

	return passwordEntropy
}
