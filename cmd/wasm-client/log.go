//go:build js

package main

import (
	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/lightning-node-connect/gbn"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"
	"google.golang.org/grpc/grpclog"
)

const Subsystem = "WASM"

var (
	log btclog.Logger
)

// SetupLoggers initializes all package-global logger variables.
func SetupLoggers(root *build.RotatingLogWriter, intercept signal.Interceptor) {
	genLogger := genSubLogger(root, intercept)

	log = build.NewSubLogger(Subsystem, genLogger)

	lnd.SetSubLogger(root, Subsystem, log)
	lnd.AddSubLogger(root, mailbox.Subsystem, intercept, mailbox.UseLogger)
	lnd.AddSubLogger(root, gbn.Subsystem, intercept, gbn.UseLogger)

	grpclog.SetLoggerV2(NewGrpcLogLogger(root, intercept, "GRPC"))
}

// genSubLogger creates a logger for a subsystem. We provide an instance of
// a signal.Interceptor to be able to shutdown in the case of a critical error.
func genSubLogger(root *build.RotatingLogWriter,
	interceptor signal.Interceptor) func(string) btclog.Logger {

	// Create a shutdown function which will request shutdown from our
	// interceptor if it is listening.
	shutdown := func() {
		if !interceptor.Listening() {
			return
		}

		interceptor.RequestShutdown()
	}

	// Return a function which will create a sublogger from our root
	// logger without shutdown fn.
	return func(tag string) btclog.Logger {
		return root.GenSubLogger(tag, shutdown)
	}
}

// NewGrpcLogLogger creates a new grpclog compatible logger and attaches it as
// a sub logger to the passed root logger.
func NewGrpcLogLogger(root *build.RotatingLogWriter,
	intercept signal.Interceptor, subsystem string) *mailbox.GrpcLogLogger {

	logger := build.NewSubLogger(subsystem, genSubLogger(root, intercept))
	lnd.SetSubLogger(root, subsystem, logger)
	return &mailbox.GrpcLogLogger{
		Logger: logger,
	}
}
