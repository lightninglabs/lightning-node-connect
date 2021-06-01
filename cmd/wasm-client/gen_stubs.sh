#!/bin/bash

function import_path() {
  local pkg=$1
  # Find out what version of pool to use for the imported auctioneer.proto by
  # parsing the go.mod file.
  LAST_LINE=$(grep "$pkg" ../../go.mod | tail -n 1)
  PKG_PATH=""
  if [[ $LAST_LINE =~ "replace" ]]
  then
    # There is a replace directive. Use awk to split by spaces then extract field
    # 4 and 5 and stitch them together with an @ which will resolve into
    # github.com/lightninglabs/pool@v0.x.x-yyyymmddhhmiss-shortcommit.
    PKG_PATH=$(echo $LAST_LINE | awk -F " " '{ print $4"@"$5 }')
  else
    # This is a normal directive, just combine field 1 and 2 with an @.
    PKG_PATH=$(echo $LAST_LINE | awk -F " " '{ print $1"@"$2 }')
  fi
  echo "$GOPATH/pkg/mod/$PKG_PATH"
}

falafel=$(which falafel)

# Name of the package for the generated APIs.
pkg="main"

# The package where the protobuf definitions originally are found.
target_pkg="github.com/lightningnetwork/lnd/lnrpc"

lnd_src=$(import_path "github.com/lightningnetwork/lnd ")
opts="package_name=$pkg,target_package=$target_pkg,api_prefix=1,js_stubs=1,build_tags=// +build js"
protoc -I/usr/local/include -I. -I"$lnd_src/lnrpc" \
       --plugin=protoc-gen-custom=$falafel\
       --custom_out=. \
       --custom_opt="$opts" \
       --proto_path="$1" \
       rpc.proto stateservice.proto

target_pkg="github.com/lightningnetwork/lnd/lnrpc/verrpc"
opts="package_name=$pkg,target_package=$target_pkg,api_prefix=1,js_stubs=1,build_tags=// +build js"
protoc -I/usr/local/include -I. -I"$lnd_src/lnrpc" \
       --plugin=protoc-gen-custom=$falafel\
       --custom_out=. \
       --custom_opt="$opts" \
       --proto_path="$1" \
       verrpc/verrpc.proto
