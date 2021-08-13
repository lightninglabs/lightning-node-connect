FROM golang:1.15.6-buster

RUN apt-get update && apt-get install -y \
  git \
  protobuf-compiler='3.6.1*' \
  clang-format='1:7.0*'

ARG PROTOBUF_VERSION
ARG GRPC_GATEWAY_VERSION

ENV PROTOC_GEN_GO_GRPC_VERSION="v1.1.0"

RUN cd /tmp \
  && export GO111MODULE=on \
  && go get google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOBUF_VERSION} \
  && go get google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION} \
  && go get github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@${GRPC_GATEWAY_VERSION} \
  && go get github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@${GRPC_GATEWAY_VERSION}

WORKDIR /build

CMD ["/bin/bash", "/build/hashmailrpc/gen_protos.sh"]
