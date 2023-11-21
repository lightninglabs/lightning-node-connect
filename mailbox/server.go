package mailbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"

	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"google.golang.org/grpc"
)

var _ net.Listener = (*Server)(nil)

type Server struct {
	serverHost string

	client hashmailrpc.HashMailClient

	mailboxConn *ServerConn

	connData *ConnData

	onNewStatus func(status ServerStatus)

	sid [64]byte

	ctx context.Context //nolint:containedctx

	quit   chan struct{}
	cancel func()
}

func NewServer(serverHost string, connData *ConnData,
	onNewStatus func(status ServerStatus),
	dialOpts ...grpc.DialOption) (*Server, error) {

	mailboxGrpcConn, err := grpc.Dial(serverHost, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to RPC server: %v",
			err)
	}

	clientConn := hashmailrpc.NewHashMailClient(mailboxGrpcConn)

	sid, err := connData.SID()
	if err != nil {
		return nil, err
	}

	s := &Server{
		serverHost:  serverHost,
		client:      clientConn,
		connData:    connData,
		sid:         sid,
		onNewStatus: onNewStatus,
		quit:        make(chan struct{}),
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

	sid, err := s.connData.SID()
	if err != nil {
		return nil, err
	}

	// If the SID has changed from what it was previously, then we close any
	// previous connection we had.
	if !bytes.Equal(s.sid[:], sid[:]) && s.mailboxConn != nil {
		err := s.mailboxConn.Stop()
		if err != nil {
			log.Errorf("could not close mailbox conn: %v", err)
		}

		s.mailboxConn = nil
	}

	s.sid = sid

	// If this is the first connection, we create a new ServerConn object.
	// otherwise, we just refresh the ServerConn.
	if s.mailboxConn == nil {
		mailboxConn, err := NewServerConn(
			s.ctx, s.serverHost, s.client, sid, s.onNewStatus,
		)
		if err != nil {
			return nil, &temporaryError{err}
		}
		s.mailboxConn = mailboxConn

	} else {
		mailboxConn, err := RefreshServerConn(s.mailboxConn)
		if err != nil {
			return nil, &temporaryError{err}
		}
		s.mailboxConn = mailboxConn
	}

	return s.mailboxConn, nil
}

// temporaryError implements the Temporary interface that grpc uses to decide
// if it should retry and reenter Accept instead of closing the server all
// together.
type temporaryError struct {
	error
}

// Temporary ensures that temporaryError satisfies the Temporary interface that
// grpc requires a returned error from the Accept function to implement so that
// it can determine if it should try again or completely shutdown the server.
func (e *temporaryError) Temporary() bool {
	return true
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
