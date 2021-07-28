package mailbox

import (
	"github.com/btcsuite/btclog"
	"github.com/lightningnetwork/lnd/build"
	"google.golang.org/grpc/grpclog"
)

const Subsystem = "MBOX"

// levelDiffToGRPCLogger is the difference in numerical log level
// definitions between the grpclog package and the btclog package.
const levelDiffToGRPCLogger = 2

// log is a logger that is initialized with no output filters.  This
// means the package will not perform any logging by default until the caller
// requests it.
var log btclog.Logger

// The default amount of logging is none.
func init() {
	UseLogger(build.NewSubLogger(Subsystem, nil))
}

// UseLogger uses a specified Logger to output package logging info.
// This should be used in preference to SetLogWriter if the caller is also
// using btclog.
func UseLogger(logger btclog.Logger) {
	log = logger
}

// GrpcLogLogger is a wrapper around a btclog logger to make it compatible with
// the grpclog logger package. By default we downgrade the info level to debug
// to reduce the verbosity of the logger.
type GrpcLogLogger struct {
	btclog.Logger
}

func (l GrpcLogLogger) Info(args ...interface{}) {
	l.Logger.Debug(args...)
}

func (l GrpcLogLogger) Infoln(args ...interface{}) {
	l.Logger.Debug(args...)
}

func (l GrpcLogLogger) Infof(format string, args ...interface{}) {
	l.Logger.Debugf(format, args...)
}

func (l GrpcLogLogger) Warning(args ...interface{}) {
	l.Logger.Warn(args...)
}

func (l GrpcLogLogger) Warningln(args ...interface{}) {
	l.Logger.Warn(args...)
}

func (l GrpcLogLogger) Warningf(format string, args ...interface{}) {
	l.Logger.Warnf(format, args...)
}

func (l GrpcLogLogger) Errorln(args ...interface{}) {
	l.Logger.Error(args...)
}

func (l GrpcLogLogger) Fatal(args ...interface{}) {
	l.Logger.Critical(args...)
}

func (l GrpcLogLogger) Fatalln(args ...interface{}) {
	l.Logger.Critical(args...)
}

func (l GrpcLogLogger) Fatalf(format string, args ...interface{}) {
	l.Logger.Criticalf(format, args...)
}

func (l GrpcLogLogger) V(level int) bool {
	return level+levelDiffToGRPCLogger >= int(l.Logger.Level())
}

// A compile-time check to make sure our GrpcLogLogger satisfies the
// grpclog.LoggerV2 interface.
var _ grpclog.LoggerV2 = (*GrpcLogLogger)(nil)
