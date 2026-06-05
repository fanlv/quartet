package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
)

var defaultLogger *slog.Logger

func init() {
	// Handler writes plain-text lines to stdout. The level guard is handled
	// upstream by enabled() so this handler accepts every record it receives.
	defaultLogger = slog.New(&plainHandler{w: os.Stdout})
}

// plainHandler outputs logs in a simple plain-text format that preserves
// newlines in messages. Level filtering and buffering are handled by the
// exported wrappers; this handler only writes what it's given.
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

// log is the shared sink for every exported wrapper. It short-circuits when
// the level is below the global threshold so we don't pay the fmt.Sprintf
// cost on disabled-level calls (Debug on a production server, for example).
func log(ctx context.Context, level slog.Level, format string, args []any) {
	lvl := int32(level)
	if !enabled(lvl) {
		return
	}
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	defaultLogger.Log(ctx, level, msg)
	logBuffer.Append(levelName(level), sourceFromCtx(ctx), msg)
}

func levelName(l slog.Level) string {
	switch l {
	case slog.LevelDebug:
		return "DEBUG"
	case slog.LevelWarn:
		return "WARN"
	case slog.LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

func Debug(format string, args ...any) { log(context.Background(), slog.LevelDebug, format, args) }
func Info(format string, args ...any)  { log(context.Background(), slog.LevelInfo, format, args) }
func Warn(format string, args ...any)  { log(context.Background(), slog.LevelWarn, format, args) }
func Error(format string, args ...any) { log(context.Background(), slog.LevelError, format, args) }

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

func Fatalf(ctx context.Context, format string, args ...any) {
	log(ctx, slog.LevelError, format, args)
	os.Exit(1)
}
