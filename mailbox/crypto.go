package mailbox

import (
	"crypto/rand"
	"crypto/sha256"
	"math/big"

	"github.com/btcsuite/btcd/btcec"
	"github.com/kkdai/bstream"
	"github.com/lightningnetwork/lnd/aezeed"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	ClientPointPreimage = "TerminalConnectClient"
	ServerPointPreimage = "TerminalConnectServer"
	NumPasswordWords    = 8 // TODO: make shorter by masking leftover bits.
	NumPasswordBytes    = (NumPasswordWords * aezeed.BitsPerWord) / 8
)

var (
	nonce = [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
)

// NewPassword generates a new one-time-use password, represented as a set of
// mnemonic words and the raw entropy itself.
func NewPassword() ([NumPasswordWords]string, [NumPasswordBytes]byte, error) {
	var (
		passwordEntropy [NumPasswordBytes]byte
		password        [NumPasswordWords]string
	)
	if _, err := rand.Read(passwordEntropy[:]); err != nil {
		return password, passwordEntropy, err
	}

	cipherBits := bstream.NewBStreamReader(passwordEntropy[:])
	for i := 0; i < NumPasswordWords; i++ {
		index, err := cipherBits.ReadBits(aezeed.BitsPerWord)
		if err != nil {
			return password, passwordEntropy, err
		}

		password[i] = aezeed.DefaultWordList[index]
	}

	return password, passwordEntropy, nil
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

// TODO(guggero): Implement in an actually secure way.
func Encrypt(plainText []byte, secret []byte) ([]byte, error) {
	cipher, _ := chacha20poly1305.New(secret)

	return cipher.Seal(nil, nonce[:], plainText, nil), nil
}

// TODO(guggero): Implement in an actually secure way.
func Decrypt(cipherText []byte, secret []byte) ([]byte, error) {
	cipher, _ := chacha20poly1305.New(secret)

	return cipher.Open(nil, nonce[:], cipherText, nil)
}
