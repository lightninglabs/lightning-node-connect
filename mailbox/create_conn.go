package mailbox

import (
	"context"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
)

// NewClientWebsocketConn attempts to create websocket LNC client connection to
// a server connection listening at the given mailbox server.
func NewClientWebsocketConn(mailboxServer, pairingPhrase string,
	localStatic keychain.SingleKeyECDH, remoteStatic *btcec.PublicKey,
	onRemoteStatic func(key *btcec.PublicKey) error,
	onAuthData func(data []byte) error) (func() ClientStatus,
	func() (*grpc.ClientConn, error), error) {

	words := strings.Split(pairingPhrase, " ")

	var mnemonicWords [NumPassphraseWords]string
	copy(mnemonicWords[:], words)
	entropy := PassphraseMnemonicToEntropy(mnemonicWords)

	connData := NewConnData(
		localStatic, remoteStatic, entropy[:], nil, onRemoteStatic,
		onAuthData,
	)

	ctx := context.Background()
	transportConn, err := NewWebsocketsClient(
		ctx, mailboxServer, connData,
	)
	if err != nil {
		return nil, nil, err
	}

	noiseConn := NewNoiseGrpcConn(connData)

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(transportConn.Dial),
		grpc.WithTransportCredentials(noiseConn),
		grpc.WithPerRPCCredentials(noiseConn),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(1024 * 1024 * 200),
		),
		grpc.WithBlock(),
	}

	return transportConn.ConnStatus, func() (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, mailboxServer, dialOpts...)
	}, nil
}
