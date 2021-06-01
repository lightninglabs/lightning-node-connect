package mailbox

import (
	"context"
	"crypto/sha512"
	"fmt"
	"net"
	"sync"

	"github.com/lightninglabs/terminal-connect/hashmailrpc"
	"google.golang.org/grpc"
)

var _ net.Listener = (*Server)(nil)

type Server struct {
	serverHost string

	mailboxGrpcConn *grpc.ClientConn
	mailboxConn     *ServerConn

	sid                  [64]byte
	ctx                  context.Context
	singleConnectionLock sync.Mutex

	cancel func()
}

func NewServer(serverHost string, password []byte,
	dialOpts ...grpc.DialOption) (*Server, error) {

	mailboxGrpcConn, err := grpc.Dial(serverHost, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to RPC server: %v",
			err)
	}

	s := &Server{
		serverHost:      serverHost,
		mailboxGrpcConn: mailboxGrpcConn,
		sid:             sha512.Sum512(password),
	}

	s.ctx, s.cancel = context.WithCancel(context.Background())

	return s, nil
}

func (s *Server) Accept() (net.Conn, error) {
	s.singleConnectionLock.Lock()

	clientConn := hashmailrpc.NewHashMailClient(s.mailboxGrpcConn)
	receiveSID := GetSID(s.sid, false)
	sendSID := GetSID(s.sid, true)

	s.mailboxConn = NewServerConn(
		s.ctx, s.serverHost, clientConn, receiveSID, sendSID,
	)

	connectMsg := NewMsgConnect(ProtocolVersion)
	if err := s.mailboxConn.ReceiveControlMsg(connectMsg); err != nil {
		return nil, err
	}

	return s.mailboxConn, nil
}

func (s *Server) Close() error {
	defer s.singleConnectionLock.Unlock()

	log.Debugf("Closing server connection")
	s.cancel()

	if s.mailboxConn != nil {
		return s.mailboxConn.Close()
	}

	return nil
}

func (s *Server) Addr() net.Addr {
	return &Addr{SID: s.sid, Server: s.serverHost}
}

func GetSID(sid [64]byte, serverToClient bool) [64]byte {
	if serverToClient {
		return sid
	}

	var clientToServerSID [64]byte
	copy(clientToServerSID[:], sid[:])
	clientToServerSID[63] ^= 0x01

	return clientToServerSID
}
