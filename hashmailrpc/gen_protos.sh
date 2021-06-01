#!/bin/bash

set -e

# generate compiles the *.pb.go stubs from the *.proto files.
function generate() {
  # Generate the gRPC bindings for all proto files.
  for file in ./*.proto; do
    protoc -I/usr/local/include -I. \
      --go_out=plugins=grpc,paths=source_relative:. \
      "${file}"
  done
}

# generate_gateway compiles the REST gateway stubs for a given file.
function generate_gateway() {
    # Generate the REST reverse proxy.
  protoc -I/usr/local/include -I. \
    --grpc-gateway_out=logtostderr=true,paths=source_relative,grpc_api_configuration="$2":. \
    "$1"

  # Finally, generate the swagger file which describes the REST API in detail.
  protoc -I/usr/local/include -I. \
    --swagger_out=logtostderr=true,grpc_api_configuration="$2":. \
    "$1"
}

# format formats the *.proto files with the clang-format utility.
function format() {
  find . -name "*.proto" -print0 | xargs -0 clang-format --style=file -i
}

# Compile and format the hashmailrpc package.
pushd hashmailrpc
format
generate
generate_gateway hashmail.proto hashmail.yaml
popd
