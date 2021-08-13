package mockrpc

import (
	"context"
	"crypto/rand"
)

type Server struct {
}

func (s *Server) MockServiceMethod(_ context.Context, req *Request) (*Response,
	error) {

	// Let the response be 10x the size of the request
	resp := make([]byte, 10*len(req.Req))
	rand.Read(resp)

	return &Response{
		Resp: resp,
	}, nil
}

var _ MockServiceServer = (*Server)(nil)
