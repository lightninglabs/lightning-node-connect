package mockrpc

import (
	"context"
)

type Server struct {
	response []byte
}

func (s *Server) SetResponse(resp []byte) {
	s.response = resp
}

func (s *Server) MockServiceMethod(_ context.Context, _ *Request) (*Response, error) {
	return &Response{
		Resp: s.response,
	}, nil
}

var _ MockServiceServer = (*Server)(nil)
