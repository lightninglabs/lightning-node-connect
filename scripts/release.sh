#!/bin/bash

set -e

DIR="$(cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd)"
BUILD_DIR=${DIR}/../build
TAG=${1}

if [ -z "${TAG}" ]; then
    echo "usage: $0 <tag>"
    echo "Example: $0 v0.1.0-alpha"
    exit 1
fi

pushd ${BUILD_DIR}

# Copy the WASM binary
cp ../reproducible-builds/wasm-client.wasm lnc-${TAG}.wasm
rm -rf ../reproducible-builds/

# Zip up the iOS and Android binaries
zip -r -X lnc-${TAG}-ios.zip ios/
zip -r -X lnc-${TAG}-android.zip android/

# Remove the iOS and Android dirs after zipping
rm -rf ios/
rm -rf android/

# Create manifest file
shasum -a 256 * >> "manifest-${TAG}.txt"

popd