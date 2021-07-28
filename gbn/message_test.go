package gbn

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMsgSerializeDeserialize(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		msg Message
	}{
		{
			msg: &PacketACK{
				128,
			},
		},
		{
			msg: &PacketData{
				10,
				[]byte{1, 2, 4, 5, 6, 7, 100},
			},
		},
		{
			msg: &PacketData{
				0,
				[]byte{},
			},
		},
		{
			msg: &PacketSYN{
				3,
			},
		},
		{
			msg: &PacketFIN{},
		},
		{
			msg: &PacketSYNACK{},
		},
	}

	for idx, tc := range testCases {
		tc := tc

		t.Run(fmt.Sprintf("%d", idx), func(t *testing.T) {
			t.Parallel()

			serialized, err := tc.msg.Serialize()
			require.NoError(t, err)

			deserialized, err := Deserialize(serialized)
			require.NoError(t, err)

			require.Equal(t, tc.msg, deserialized)
		})
	}
}
