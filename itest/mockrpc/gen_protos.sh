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

# format formats the *.proto files with the clang-format utility.
function format() {
  find . -name "*.proto" -print0 | xargs -0 clang-format --style=file -i
}

# Compile and format the mockrpc package.
pushd mockrpc
format
generate
popd
