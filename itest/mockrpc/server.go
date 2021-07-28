package mockrpc

import (
	"context"
)

type Server struct{}

func (s Server) SayHello(ctx context.Context, req *SayHelloReq) (*SayHelloResp, error) {
	return &SayHelloResp{
		Hello: "Wassup",
	}, nil
}

var _ HelloServer = (*Server)(nil)
