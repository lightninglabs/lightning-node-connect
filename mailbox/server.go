package mailbox

import (
	"context"
	"crypto/sha512"
	"fmt"
	"io"
	"net"

	"github.com/lightninglabs/terminal-connect/hashmailrpc"
	"google.golang.org/grpc"
)

var _ net.Listener = (*Server)(nil)

type Server struct {
	serverHost string

	client hashmailrpc.HashMailClient

	mailboxConn *ServerConn

	sid [64]byte
	ctx context.Context

	quit   chan struct{}
	cancel func()
}

func NewServer(serverHost string, password []byte,
	dialOpts ...grpc.DialOption) (*Server, error) {

	mailboxGrpcConn, err := grpc.Dial(serverHost, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to RPC server: %v",
			err)
	}

	clientConn := hashmailrpc.NewHashMailClient(mailboxGrpcConn)

	s := &Server{
		serverHost: serverHost,
		client:     clientConn,
		sid:        sha512.Sum512(password),
		quit:       make(chan struct{}),
	}

	s.ctx, s.cancel = context.WithCancel(context.Background())

	return s, nil
}

// Accept is part of the net.Listener interface. The gRPC server will call this
// function to get a new net.Conn object to use for communication and it will
// also call this function each time it returns in order to facilitate multiple
// concurrent grpc connections. In our use case, we require that only one
// connection is active at a time. Therefore we block on a select function until
// the previous mailboxConn has completed.
func (s *Server) Accept() (net.Conn, error) {
	select {
	case <-s.ctx.Done():
		return nil, io.EOF
	default:
	}

	// If there is currently an active connection, block here until the
	// previous connection as been closed.
	if s.mailboxConn != nil {
		log.Debugf("Accept: have existing mailbox connection, waiting")
		select {
		case <-s.quit:
			return nil, io.EOF
		case <-s.mailboxConn.Done():
			log.Debugf("Accept: done with existing conn")
		}
	}

	log.Debugf("new conn entering")

	receiveSID := GetSID(s.sid, false)
	sendSID := GetSID(s.sid, true)

	// If this is the first connection, we create a new ServerConn object.
	// otherwise, we just refresh the ServerConn.
	var err error
	if s.mailboxConn == nil {
		s.mailboxConn, err = NewServerConn(
			s.ctx, s.serverHost, s.client, receiveSID, sendSID,
		)
		if err != nil {
			log.Errorf("couldn't create new server: %v", err)
			return nil, err
		}
	} else {
		s.mailboxConn, err = RefreshServerConn(s.mailboxConn)
		if err != nil {
			log.Errorf("couldn't refresh server: %v", err)
			return nil, err
		}
	}

	log.Debugf("new conn succeeded")

	return s.mailboxConn, nil
}

func (s *Server) Close() error {
	log.Debugf("conn being closed")

	close(s.quit)

	if s.mailboxConn != nil {
		if err := s.mailboxConn.Stop(); err != nil {
			log.Errorf("error closing mailboxConn %v", err)
		}
	}
	s.cancel()
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
