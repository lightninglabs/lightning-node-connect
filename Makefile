PKG := github.com/lightninglabs/lightning-node-connect

LINT_PKG := github.com/golangci/golangci-lint/cmd/golangci-lint
GOVERALLS_PKG := github.com/mattn/goveralls
GOACC_PKG := github.com/ory/go-acc

GO_BIN := ${GOPATH}/bin
LINT_BIN := $(GO_BIN)/golangci-lint

LINT_COMMIT := v1.18.0

DEPGET := cd /tmp && GO111MODULE=on go get -v
GOBUILD := go build -v
GOINSTALL := go install -v
GOTEST := GO111MODULE=on go test -v

GOFILES_NOVENDOR = $(shell find . -type f -name '*.go' -not -path "./vendor/*")

GOLIST := go list $(PKG)/... | grep -v '/vendor/'
XARGS := xargs -L 1

LDFLAGS := -s -w -buildid=
LDFLAGS_MOBILE := -ldflags "$(call make_ldflags, ${tags}, -s -w)"

RM := rm -f
CP := cp
MAKE := make
XARGS := xargs -L 1

LINT = $(LINT_BIN) run -v --build-tags itest

PKG := github.com/lightninglabs/lightning-node-connect
MOBILE_PKG := $(PKG)/mobile
MOBILE_BUILD_DIR :=${GOPATH}/src/$(PKG)/build
IOS_BUILD_DIR := $(MOBILE_BUILD_DIR)/ios
IOS_BUILD := $(IOS_BUILD_DIR)/Lncmobile.xcframework
ANDROID_BUILD_DIR := $(MOBILE_BUILD_DIR)/android
ANDROID_BUILD := $(ANDROID_BUILD_DIR)/lnc-mobile.aar

GOMOBILE_BIN := $(GO_BIN)/gomobile

RPC_TAGS := appengine autopilotrpc chainrpc invoicesrpc neutrinorpc peersrpc signrpc wtclientrpc watchtowerrpc routerrpc walletrpc verrpc

include make/testing_flags.mk

default: build

all: build check

# ============
# DEPENDENCIES
# ============

$(LINT_BIN):
	@$(call print, "Fetching linter")
	$(DEPGET) $(LINT_PKG)@$(LINT_COMMIT)

# ============
# INSTALLATION
# ============

build:
	@$(call print, "Building lightning-node-connect.")
	$(GOBUILD) -tags="$(RPC_TAGS)" $(PKG)/...

wasm:
	# The appengine build tag is needed because of the jessevdk/go-flags library
	# that has some OS specific terminal code that doesn't compile to WASM.
	cd cmd/wasm-client; CGO_ENABLED=0 GOOS=js GOARCH=wasm go build -trimpath -ldflags="$(LDFLAGS)" -tags="$(RPC_TAGS)" -v -o wasm-client.wasm .
	$(CP) cmd/wasm-client/wasm-client.wasm example/wasm-client.wasm

apple:
	@$(call print, "Building iOS and macOS cxframework ($(IOS_BUILD)).")
	mkdir -p $(IOS_BUILD_DIR)
	$(GOMOBILE_BIN) bind -target=ios,iossimulator,macos -tags="mobile $(DEV_TAGS) $(RPC_TAGS)" $(LDFLAGS_MOBILE) -v -o $(IOS_BUILD) $(MOBILE_PKG)

ios:
	@$(call print, "Building iOS cxframework ($(IOS_BUILD)).")
	mkdir -p $(IOS_BUILD_DIR)
	$(GOMOBILE_BIN) bind -target=ios,iossimulator -tags="mobile $(DEV_TAGS) $(RPC_TAGS)" $(LDFLAGS_MOBILE) -v -o $(IOS_BUILD) $(MOBILE_PKG)

macos:
	@$(call print, "Building macOS cxframework ($(IOS_BUILD)).")
	mkdir -p $(IOS_BUILD_DIR)
	$(GOMOBILE_BIN) bind -target=macos -tags="mobile $(DEV_TAGS) $(RPC_TAGS)" $(LDFLAGS_MOBILE) -v -o $(IOS_BUILD) $(MOBILE_PKG)

android:
	@$(call print, "Building Android library ($(ANDROID_BUILD)).")
	mkdir -p $(ANDROID_BUILD_DIR)
	GOOS=js $(GOMOBILE_BIN) bind -target=android -tags="mobile $(DEV_TAGS) $(RPC_TAGS)" -androidapi 21 $(LDFLAGS_MOBILE) -v -o $(ANDROID_BUILD) $(MOBILE_PKG)

mobile: ios android

repro-wasm:
	#Build the repro-wasm image
	docker build -f Dockerfile-wasm -t repro-wasm-image --no-cache .

	#Run the repro-wasm-image in a new container called repro-wasm
	docker run --name repro-wasm  repro-wasm-image

	#Copy the compiled WASM file to the host machine
	mkdir -p reproducible-builds
	docker cp repro-wasm:/app/cmd/wasm-client/wasm-client.wasm ./reproducible-builds/
	
	#Remove the repro-wasm container
	docker rm repro-wasm

	#Remove the repro-wasm-image
	docker image rm repro-wasm-image

# =======
# TESTING
# =======

check: unit

unit:
	@$(call print, "Running unit tests.")
	$(UNIT) -tags="$(RPC_TAGS)"

unit-race:
	@$(call print, "Running unit race tests.")
	env CGO_ENABLED=1 GORACE="history_size=7 halt_on_errors=1" $(UNIT_RACE) -tags="$(RPC_TAGS)"

itest: itest-run

itest-run:
	@$(call print, "Running integration tests.")
	$(GOTEST) ./itest -tags="$(ITEST_TAGS)" $(TEST_FLAGS) -logoutput -goroutinedump -logdir=itest_logs

# =========
# UTILITIES
# =========
fmt:
	@$(call print, "Formatting source.")
	gofmt -l -w -s $(GOFILES_NOVENDOR)

lint: $(LINT_BIN)
	@$(call print, "Linting source.")
	$(LINT)

list:
	@$(call print, "Listing commands.")
	@$(MAKE) -qp | \
		awk -F':' '/^[a-zA-Z0-9][^$$#\/\t=]*:([^=]|$$)/ {split($$1,A,/ /);for(i in A)print A[i]}' | \
		grep -v Makefile | \
		sort

rpc:
	@$(call print, "Compiling protos.")
	cd ./hashmailrpc; ./gen_protos_docker.sh

rpc-check: rpc
	@$(call print, "Verifying protos.")
	if test -n "$$(git describe --dirty | grep dirty)"; then echo "Protos not properly formatted or not compiled with correct version!"; git status; git diff; exit 1; fi

example-server:
	go run example-server.go example/ 8080
