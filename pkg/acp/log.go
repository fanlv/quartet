package acp

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	acp "github.com/eino-contrib/acp"

	"github.com/fanlv/quartet/pkg/logger"
)

func init() {
	// Let pkg/logger be the single source of truth for runtime log filtering.
	// If the SDK is configured with LevelInfo here, ACP debug logs are discarded
	// before they can reach pkg/logger, so QUARTET_LOG_LEVEL=debug and the
	// runtime log-level API cannot reveal SDK-side diagnostics.
	acp.SetLogger(&defaultLogger{}, acp.LevelDebug)
}

var _ acp.Logger = (*defaultLogger)(nil)

type defaultLogger struct {
}

// CtxDebug implements [Logger].
func (d *defaultLogger) CtxDebug(ctx context.Context, format string, v ...interface{}) {
	logger.Debugf(ctx, format, v...)
}

// CtxError implements [Logger].
func (d *defaultLogger) CtxError(ctx context.Context, format string, v ...interface{}) {
	if isBenignStdioCloseLog(format, v) {
		logger.Debugf(ctx, format, v...)
		return
	}
	logger.Errorf(ctx, format, v...)
}

// CtxInfo implements [Logger].
func (d *defaultLogger) CtxInfo(ctx context.Context, format string, v ...interface{}) {
	logger.Infof(ctx, format, v...)
}

// CtxWarn implements [Logger].
func (d *defaultLogger) CtxWarn(ctx context.Context, format string, v ...interface{}) {
	logger.Warnf(ctx, format, v...)
}

// Debug implements [Logger].
func (d *defaultLogger) Debug(format string, v ...interface{}) {
	logger.Debug(format, v...)
}

// Error implements [Logger].
func (d *defaultLogger) Error(format string, v ...interface{}) {
	if isBenignStdioCloseLog(format, v) {
		logger.Debug(format, v...)
		return
	}
	logger.Error(format, v...)
}

// Info implements [Logger].
func (d *defaultLogger) Info(format string, v ...interface{}) {
	logger.Info(format, v...)
}

// Warn implements [Logger].
func (d *defaultLogger) Warn(format string, v ...interface{}) {
	logger.Warn(format, v...)
}

// isBenignStdioCloseLog matches the SDK's stdio transport teardown noise:
// when the subprocess exits, exec.Cmd.Wait() closes the stdin/stdout pipes
// before Transport.Close() runs, so the SDK's defensive c.Close() returns
// os.ErrClosed and gets logged at Error level. The teardown is correct —
// reconnectIfNeeded transparently spins up a new connection — but the line
// pollutes the ERROR stream and can't be silenced upstream from here. The
// SDK already filters the equivalent for the WS proxy via isBenignCloseErr;
// this is the stdio counterpart applied at the logger boundary.
//
// Match is conservative: format must be the SDK's exact stdio-close phrase
// (with or without the SDK's "[ACP-SDK] " prefix), and the trailing error
// must be a known benign close. Any other Error log from the SDK keeps its
// original level.
func isBenignStdioCloseLog(format string, v []interface{}) bool {
	if !strings.HasSuffix(format, "close stdio reader: %v") &&
		!strings.HasSuffix(format, "close stdio writer: %v") {
		return false
	}
	if len(v) == 0 {
		return false
	}
	err, ok := v[len(v)-1].(error)
	if !ok || err == nil {
		return false
	}
	return errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}
