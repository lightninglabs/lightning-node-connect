package gbn

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQueueSize(t *testing.T) {
	q := newQueue(&queueCfg{s: 4})

	require.Equal(t, uint8(0), q.size())

	q.sequenceBase = 2
	q.sequenceTop = 3
	require.Equal(t, uint8(1), q.size())

	q.sequenceBase = 3
	q.sequenceTop = 2
	require.Equal(t, uint8(3), q.size())
}
