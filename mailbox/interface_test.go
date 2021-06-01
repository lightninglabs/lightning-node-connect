package mailbox

import (
	"fmt"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/stretchr/testify/require"
)

var (
	privKey, _ = btcec.NewPrivateKey(btcec.S256())
	pubKey     = privKey.PubKey()
)

func TestControlMsgSerializeDeserialize(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		src ControlMsg
		dst ControlMsg
	}{{
		src: NewMsgConnect(123),
		dst: &MsgConnect{},
	}, {
		src: NewMsgClientHello(255, pubKey),
		dst: &MsgClientHello{},
	}, {
		src: NewMsgServerHello(0, pubKey, []byte{
			77, 88, 99, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
		}),
		dst: &MsgServerHello{},
	}, {
		src: NewMsgServerHello(0, pubKey, nil),
		dst: &MsgServerHello{},
	}, {
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
