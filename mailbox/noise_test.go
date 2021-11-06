package mailbox

import (
	"crypto/sha256"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/stretchr/testify/require"
)

// TestSpake2Mask tests the masking operation for SPAK2 to ensure that ti's
// properly reverseable.
func TestSpake2Mask(t *testing.T) {
	t.Parallel()

	priv, err := btcec.NewPrivateKey(btcec.S256())
	require.NoError(t, err)

	pub := priv.PubKey()

	pass := []byte("top secret")
	passHash := sha256.Sum256(pass)

	maskedPoint := ekeMask(pub, passHash[:])
	require.True(t, !maskedPoint.IsEqual(pub))

	unmaskedPoint := ekeUnmask(maskedPoint, passHash[:])
	require.True(t, unmaskedPoint.IsEqual(pub))
}
