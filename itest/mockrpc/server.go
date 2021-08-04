package mockrpc

import (
	"context"
)

type Server struct {
}

func (s *Server) MockServiceMethod(_ context.Context, req *Request) (*Response,
	error) {

	return &Response{
		Resp: req.Req,
	}, nil
}

var _ MockServiceServer = (*Server)(nil)
