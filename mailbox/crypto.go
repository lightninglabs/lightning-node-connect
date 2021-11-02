package mailbox

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"

	hash2curve "github.com/armfazh/h2c-go-ref"
	"github.com/btcsuite/btcd/btcec"
	"github.com/kkdai/bstream"
	"github.com/lightningnetwork/lnd/aezeed"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	ClientPointPreimage = "TerminalConnectClient"
	ServerPointPreimage = "TerminalConnectServer"

	// PasswordHKDFSalt is the static salt we use for stretching a TC
	// one-time-password with the HKDF algorithm.
	PasswordHKDFSalt = "TerminalConnectPassword"

	// Hash2CurveAlgo is the algorithm we use for hashing a value to our
	// secp256k1 curve, using SHA256, Simplified SWU and the Random Oracle
	// model.
	Hash2CurveAlgo = hash2curve.Secp256k1_XMDSHA256_SSWU_RO_

	NumPasswordWords = 8 // TODO: make shorter by masking leftover bits.
	NumPasswordBytes = (NumPasswordWords * aezeed.BitsPerWord) / 8
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

// PasswordEntropyToMnemonic turns a password's raw entropy bytes into the
// mnemonic representation that is human-readable.
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

// Hash2Curve uses the hash2curve library referenced in
// https://datatracker.ietf.org/doc/draft-irtf-cfrg-hash-to-curve/ to generate
// a point on the secp256k1 curve that nobody knows the discrete log of by
// hashing the given string to the curve.
func Hash2Curve(preimage []byte) (*btcec.PublicKey, error) {
	var pubKey [33]byte
	h2p, err := Hash2CurveAlgo.Get(pubKey[:])
	if err != nil {
		return nil, fmt.Errorf("error hashing to point: %v", err)
	}

	point := h2p.Hash(preimage)
	ecPoint := &btcec.PublicKey{
		Curve: btcec.S256(),
		X:     point.X().Polynomial()[0],
		Y:     point.Y().Polynomial()[0],
	}
	return ecPoint, nil
}

// SPAKE2Mask masks an ephemeral public key by adding the product of the
// generator point and the stretched password.
//
// SPAKE2Mask(e, pw):
//   e + N*pw
func SPAKE2Mask(ephemeralPubKey *btcec.PublicKey,
	generatorPointPreimage string, password []byte) (*btcec.PublicKey,
	error) {

	// Now we calculate P = e + N*pw, first step is N' = N*pw.
	nPrime, err := blindingPoint(generatorPointPreimage, password)
	if err != nil {
		return nil, err
	}

	// Second step is P = e + N' which is
	// result = ephemeralPubKey + blindingPoint
	result := &btcec.PublicKey{Curve: btcec.S256()}
	result.X, result.Y = btcec.S256().Add(
		nPrime.X, nPrime.Y, ephemeralPubKey.X, ephemeralPubKey.Y,
	)
	return result, nil
}

// SPAKE2Unmask unmasks a previously blinded point by subtracting the product of
// the generator point and the stretched password from the blinded key.
//
// SPAKE2Unmask(e, pw):
//   e - N*pw
func SPAKE2Unmask(blindedKey *btcec.PublicKey,
	generatorPointPreimage string, password []byte) (*btcec.PublicKey,
	error) {

	// Now we calculate P = e - N*pw, first step is N' = N*pw.
	nPrime, err := blindingPoint(generatorPointPreimage, password)
	if err != nil {
		return nil, err
	}

	// With EC we need to calculate P = e + (-N') as there is no
	// subtraction, only inversion.
	result := &btcec.PublicKey{Curve: btcec.S256()}
	negY := new(big.Int).Neg(nPrime.Y)
	negY = negY.Mod(negY, btcec.S256().P)

	// Now we can calculate P = e + (-N') which is
	// result = blindedKey + (-nPrime)
	result.X, result.Y = blindedKey.Curve.Add(
		blindedKey.X, blindedKey.Y, nPrime.X, negY,
	)
	return result, nil
}

// blindingPoint generates the blinding point from the client or server specific
// generator point preimage and the stretched one-time-password.
func blindingPoint(generatorPointPreimage string,
	password []byte) (*btcec.PublicKey, error) {

	// First we create our generator point (M or N depending on whether we
	// are the client or server) by hashing to the curve so nobody knows the
	// discrete log for that generator point.
	generator, err := Hash2Curve([]byte(generatorPointPreimage))
	if err != nil {
		return nil, err
	}

	// We use HKDF with a static salt to stretch the password to get some
	// additional entropy.
	const pwLen = 32
	var stretchedPassword [pwLen]byte
	h := hkdf.New(sha256.New, password, []byte(PasswordHKDFSalt), []byte{})
	n, err := h.Read(stretchedPassword[:])
	if n != pwLen {
		return nil, fmt.Errorf("not enough entropy to stretch password")
	}
	if err != nil {
		return nil, fmt.Errorf("error stretching password: %v", err)
	}

	// Now we calculate N' = N*pw which is
	// result = generator*stretchedPassword.
	result := &btcec.PublicKey{}
	result.X, result.Y = btcec.S256().ScalarMult(
		generator.X, generator.Y, stretchedPassword[:],
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
