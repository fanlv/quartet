// Package logger is the eino-cli fork of quartet's pkg/logger, reduced to the
// surface the eino chain uses (Debugf/Infof/Warnf/Errorf/DebugEnabled).
//
// CRITICAL DIFFERENCE from pkg/logger: all output goes to STDERR, never
// stdout. When eino-cli runs as an ACP server, stdout is the JSON-RPC
// channel — a single stray log line on stdout corrupts the protocol stream.
//
// The default level is Info; set EINO_CLI_DEBUG=1 to enable Debug output.
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

var defaultLogger *slog.Logger

// plainHandler outputs logs in a simple plain-text format that preserves
// newlines in messages. Level filtering is handled by the exported wrappers;
// this handler only writes what it's given.
type plainHandler struct {
	w io.Writer
}

func (h *plainHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *plainHandler) Handle(_ context.Context, r slog.Record) error {
	ts := r.Time.Format(time.DateTime)
	_, err := fmt.Fprintf(h.w, "%s %s %s\n", ts, r.Level.String(), r.Message)
	return err
}

func (h *plainHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *plainHandler) WithGroup(_ string) slog.Handler      { return h }

// Level guard. Stored as an int32 so calls can read it without a mutex.
// Default is Info; EINO_CLI_DEBUG=1 (checked in init) drops it to Debug.
var currentLevel atomic.Int32

const (
	levelDebug = int32(slog.LevelDebug)
	levelInfo  = int32(slog.LevelInfo)
)

func init() {
	currentLevel.Store(levelInfo)
	// Handler writes plain-text lines to stderr. The level guard is handled
	// upstream by enabled() so this handler accepts every record it receives.
	defaultLogger = slog.New(&plainHandler{w: os.Stderr})
	if os.Getenv("EINO_CLI_DEBUG") == "1" {
		currentLevel.Store(levelDebug)
	}
}

func enabled(level int32) bool {
	return level >= currentLevel.Load()
}

// DebugEnabled reports whether Debug-level output is currently on.
// Use this to gate expensive argument construction (e.g. JSON serialization)
// BEFORE calling Debugf — Go evaluates arguments before the callee, so the
// level guard inside log() can't short-circuit that work.
func DebugEnabled() bool {
	return enabled(levelDebug)
}

// log is the shared sink for every exported wrapper. It short-circuits when
// the level is below the global threshold so we don't pay the fmt.Sprintf
// cost on disabled-level calls.
func log(ctx context.Context, level slog.Level, format string, args []any) {
	if !enabled(int32(level)) {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	defaultLogger.Log(ctx, level, msg)
}

func Debugf(ctx context.Context, format string, args ...any) {
	log(ctx, slog.LevelDebug, format, args)
}

func Infof(ctx context.Context, format string, args ...any) {
	log(ctx, slog.LevelInfo, format, args)
}

func Warnf(ctx context.Context, format string, args ...any) {
	log(ctx, slog.LevelWarn, format, args)
}

func Errorf(ctx context.Context, format string, args ...any) {
	log(ctx, slog.LevelError, format, args)
}
