package mailbox

import (
	"crypto/rand"
	"runtime/debug"

	"github.com/kkdai/bstream"
	"github.com/lightningnetwork/lnd/aezeed"
	"golang.org/x/crypto/scrypt"
)

const (
	// NumPassphraseWords is the number of words we use for the pairing
	// phrase.
	NumPassphraseWords = 10

	// NumPassphraseEntropyBytes is the number of bytes we use for the
	// pairing phrase. This must be:
	//   ceil( (NumPassphraseWords * aezeed.BitsPerWord) / 8 )
	NumPassphraseEntropyBytes = 14

	// scryptKeyLen is the amount of bytes we'll generate from the scrpt
	// invocation. Using the passphrase entropy as the passphraseEntropy and the
	// salt.
	scryptKeyLen = 32
)

var (
	// Below are the default scrypt parameters that are tied to the initial
	// version 0 of the noise handshake.
	scryptN = 1 << 16 // 65536
	scryptR = 8
	scryptP = 1
)

// NewPassphraseEntropy generates a new one-time-use passphrase, represented as
// a set of mnemonic words and the raw entropy itself.
func NewPassphraseEntropy() ([NumPassphraseWords]string,
	[NumPassphraseEntropyBytes]byte, error) {

	var (
		passphraseEntropy [NumPassphraseEntropyBytes]byte
		passphrase        [NumPassphraseWords]string
		err               error
	)
	if _, err = rand.Read(passphraseEntropy[:]); err != nil {
		return passphrase, passphraseEntropy, err
	}

	// Turn the raw bytes into words. Since we read full bytes above but
	// might only use some bits of the last byte the words won't contain
	// the full data.
	passphrase, err = PassphraseEntropyToMnemonic(passphraseEntropy)
	if err != nil {
		return passphrase, passphraseEntropy, err
	}

	// To make sure the words and raw bytes match, we convert the words
	// back into raw bytes, effectively setting the last, unused bits to
	// zero.
	passphraseEntropy = PassphraseMnemonicToEntropy(passphrase)

	return passphrase, passphraseEntropy, nil
}

// PassphraseEntropyToMnemonic turns the raw bytes of the passphrase entropy
// into human-readable mnemonic words.
// NOTE: This will only use NumPassphraseWords * aezeed.BitsPerWord bits of the
// provided entropy.
func PassphraseEntropyToMnemonic(
	entropy [NumPassphraseEntropyBytes]byte) ([NumPassphraseWords]string,
	error) {

	var (
		passphrase [NumPassphraseWords]string
		cipherBits = bstream.NewBStreamReader(entropy[:])
	)
	for i := 0; i < NumPassphraseWords; i++ {
		index, err := cipherBits.ReadBits(aezeed.BitsPerWord)
		if err != nil {
			return passphrase, err
		}

		passphrase[i] = aezeed.DefaultWordList[index]
	}

	return passphrase, nil
}

// PassphraseMnemonicToEntropy reverses the mnemonic word encoding and returns
// the raw passphrase entropy bytes.
// NOTE: This will only set the first NumPassphraseWords * aezeed.BitsPerWord
// bits of the entropy. The remaining bits will be set to zero.
func PassphraseMnemonicToEntropy(
	passphrase [NumPassphraseWords]string) [NumPassphraseEntropyBytes]byte {

	var passphraseEntropy [NumPassphraseEntropyBytes]byte
	cipherBits := bstream.NewBStreamWriter(NumPassphraseEntropyBytes)
	for _, word := range passphrase {
		index := uint64(aezeed.ReverseWordMap[word])
		cipherBits.WriteBits(index, aezeed.BitsPerWord)
	}

	copy(passphraseEntropy[:], cipherBits.Bytes())

	return passphraseEntropy
}

// stretchPassphrase takes a randomly generated passphrase entropy and runs it
// through scrypt with our specified parameters.
func stretchPassphrase(passphraseEntropy []byte) ([]byte, error) {
	// Note that we use the passphrase entropy again as the salt itself, as
	// we always generate the pairing phrase from a high entropy source.
	rawPairingBytes, err := scrypt.Key(
		passphraseEntropy, passphraseEntropy, scryptN, scryptR, scryptP,
		scryptKeyLen,
	)
	if err != nil {
		return nil, err
	}

	// This ends up generating a lot of memory, so we'll actually force a
	// manual GC collection here to keep down the memory constraints.
	debug.FreeOSMemory()

	return rawPairingBytes, nil
}
