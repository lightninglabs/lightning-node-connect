package mailbox

import (
	"crypto/rand"
	"runtime/debug"

	"github.com/kkdai/bstream"
	"github.com/lightningnetwork/lnd/aezeed"
	"golang.org/x/crypto/scrypt"
)

const (
	// NumPasswordWords is the number of words we use for the pairing
	// phrase.
	NumPasswordWords = 10
	
	// NumPasswordBytes is the number of bytes we use for the pairing
	// phrase. This must be:
	//   ceil( (NumPasswordWords * aezeed.BitsPerWord) / 8 )
	NumPasswordBytes = 14

	// scryptKeyLen is the amount of bytes we'll generate from the scrpt
	// invocation. Using the password as the password and the salt.
	scryptKeyLen = 32
)

var (
	// Below are the default scrypt parameters that are tied to the initial
	// version 0 of the noise handshake.
	scryptN = 1 << 16 // 65536
	scryptR = 8
	scryptP = 1
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

	// Turn the raw bytes into words. Since we read full bytes above but
	// might only use some bits of the last byte the words won't contain
	// the full data.
	password, err = PasswordEntropyToMnemonic(passwordEntropy)
	if err != nil {
		return password, passwordEntropy, err
	}
	
	// To make sure the words and raw bytes match, we convert the words
	// back into raw bytes, effectively setting the last, unused bits to
	// zero.
	passwordEntropy = PasswordMnemonicToEntropy(password)

	return password, passwordEntropy, nil
}

// PasswordEntropyToMnemonic turns the raw bytes of a password entropy into
// human-readable mnemonic words.
// NOTE: This will only use NumPasswordWords * aezeed.BitsPerWord bits of the
// provided entropy.
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
// NOTE: This will only set the first NumPasswordWords * aezeed.BitsPerWord bits
// of the entropy. The remaining bits will be set to zero.
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

// stretchPassword takes a randomly generated passphrase and runs it through
// scrypt with our specified parameters.
func stretchPassword(password []byte) ([]byte, error) {
	// Note that we use the password again as the salt itself, as we always
	// generate the pairing phrase from a high entropy source.
	rawPairingBytes, err := scrypt.Key(
		password, password, scryptN, scryptR, scryptP, scryptKeyLen,
	)
	if err != nil {
		return nil, err
	}

	// This ends up generating a lot of memory, so we'll actually force a
	// manual GC collection here to keep down the memory constraints.
	debug.FreeOSMemory()

	return rawPairingBytes, nil
}
