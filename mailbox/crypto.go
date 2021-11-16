package mailbox

import (
	"crypto/rand"
	"runtime/debug"

	"github.com/kkdai/bstream"
	"github.com/lightningnetwork/lnd/aezeed"
	"golang.org/x/crypto/scrypt"
)

const (
	NumPasswordWords = 8 // TODO: make shorter by masking leftover bits.
	NumPasswordBytes = (NumPasswordWords * aezeed.BitsPerWord) / 8

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
