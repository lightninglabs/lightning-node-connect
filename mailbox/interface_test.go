package mailbox

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControlMsgSerializeDeserialize(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		src ControlMsg
		dst ControlMsg
	}{{
		src: NewMsgData(123, []byte{
			77, 88, 99, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12,
			13, 14, 15, 16, 17, 0, 0, 0, 0, 0, 0, 99, 88, 77, 66,
		}),
		dst: &MsgData{},
	}, {
		src: NewMsgData(123, nil),
		dst: &MsgData{},
	}}

	for idx, tc := range testCases {
		tc := tc

		t.Run(fmt.Sprintf("%d", idx), func(t *testing.T) {
			t.Parallel()

			serialized, err := tc.src.Serialize()
			require.NoError(t, err)

			err = tc.dst.Deserialize(serialized)
			require.NoError(t, err)

			require.Equal(t, tc.src, tc.dst)
		})
	}
}
