package mailbox

import (
	"crypto/rand"
	"crypto/sha256"
	"math/big"

	"github.com/btcsuite/btcd/btcec"
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

// TODO(guggero): Implement using "hash-and-increment" algorithm.
func GeneratorPoint(preimage string) (*btcec.PublicKey, error) {
	hash := sha256.Sum256([]byte(preimage))
	_, pubKey := btcec.PrivKeyFromBytes(btcec.S256(), hash[:])
	return pubKey, nil
}

// TODO(guggero): Implement in an actually secure way.
func SPAKE2MaskPoint(ephemeralPubKey *btcec.PublicKey,
	generatorPointPreimage string, password []byte) (*btcec.PublicKey,
	error) {

	g, err := GeneratorPoint(generatorPointPreimage)
	if err != nil {
		return nil, err
	}

	// Use PBKDF2 here?
	pwHash := sha256.Sum256(password)
	blindingPoint := &btcec.PublicKey{}
	blindingPoint.X, blindingPoint.Y = btcec.S256().ScalarMult(
		g.X, g.Y, pwHash[:],
	)

	result := &btcec.PublicKey{Curve: btcec.S256()}
	result.X, result.Y = btcec.S256().Add(
		blindingPoint.X, blindingPoint.Y,
		ephemeralPubKey.X, ephemeralPubKey.Y,
	)
	return result, nil
}

// TODO(guggero): Implement in an actually secure way.
func SPAKE2UnmaskPoint(blindedKey *btcec.PublicKey,
	generatorPointPreimage string, password []byte) (*btcec.PublicKey,
	error) {

	g, err := GeneratorPoint(generatorPointPreimage)
	if err != nil {
		return nil, err
	}

	// Use PBKDF2 here?
	pwHash := sha256.Sum256(password)
	blindingPoint := &btcec.PublicKey{}
	blindingPoint.X, blindingPoint.Y = btcec.S256().ScalarMult(
		g.X, g.Y, pwHash[:],
	)

	result := &btcec.PublicKey{Curve: btcec.S256()}
	negY := new(big.Int).Neg(blindedKey.Y)
	negY = negY.Mod(negY, btcec.S256().P)
	result.X, result.Y = blindedKey.Curve.Add(
		blindedKey.X, blindedKey.Y, blindedKey.X, negY,
	)
	return result, nil
}
