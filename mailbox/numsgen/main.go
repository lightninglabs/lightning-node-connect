package main

import (
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
)

const (
	defaultSeedPhrase = "Lightning Node Connect"
)

var (
	seedPhrase = flag.String("seed_phrase", defaultSeedPhrase,
		"the starting seed phrase that binds the global params to "+
			"a particular context")
)

func increment(seedPhrase []byte, i int) []byte {
	// Increment the origin of the hash using the mapping candidateSed :=
	// h(i || seedPhrase).
	shaStream := sha256.New()

	var iterationBytes [8]byte
	binary.BigEndian.PutUint64(iterationBytes[:], uint64(i))
	shaStream.Write(iterationBytes[:])

	shaStream.Write(seedPhrase)

	seedHash := shaStream.Sum(nil)

	return seedHash[:]
}

func main() {
	flag.Parse()

	// Trim any space before and after the target seed phrase.
	*seedPhrase = strings.TrimSpace(*seedPhrase)
	fmt.Printf("Using seed phrase: %v\n", *seedPhrase)

	var i int
	for {
		// At the start of the iteration, we'll try with a counter
		// offset of 0, each time we fail to find a proper point, we'll
		// increment this by one.
		candidateSeed := increment([]byte(*seedPhrase), i)

		fmt.Printf("iteration_num=%v, candidate=%x\n", i, candidateSeed[:])

		var pubBytes [33]byte
		pubBytes[0] = 0x02
		copy(pubBytes[1:], candidateSeed[:])

		// Try to see if this point when intercepted as an x coordinate
		// is actually found on the curve.
		candidatePoint, err := btcec.ParsePubKey(pubBytes[:])
		if err == nil {
			fmt.Printf("Global param generated: %x\n",
				candidatePoint.SerializeCompressed())
			break
		}

		fmt.Printf("not a quadratic residue: %v\n", err)

		i++
	}
}
