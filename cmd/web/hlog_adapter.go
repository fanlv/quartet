package main

import (
	"context"
	"fmt"
	"io"

	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/fanlv/quartet/pkg/logger"
)

// hlogBridge forwards every hlog call into pkg/logger so Hertz's internal
// lines share the project's timestamp/level format instead of the default
// `2026/05/07 17:19:17.873988 engine.go:416: [Info] HERTZ: ...` line. Without
// this bridge the backend log interleaves two unrelated formats and is
// painful to grep.
type hlogBridge struct {
	level hlog.Level
}

func (b *hlogBridge) SetOutput(_ io.Writer) {}
func (b *hlogBridge) SetLevel(lv hlog.Level) {
	b.level = lv
}

func (b *hlogBridge) enabled(lv hlog.Level) bool { return lv >= b.level }

func (b *hlogBridge) emit(ctx context.Context, lv hlog.Level, msg string) {
	if !b.enabled(lv) {
		return
	}
	msg = "[hertz] " + msg
	switch {
	case lv >= hlog.LevelError:
		logger.Errorf(ctx, "%s", msg)
	case lv == hlog.LevelWarn:
		logger.Warnf(ctx, "%s", msg)
	case lv == hlog.LevelDebug, lv == hlog.LevelTrace:
		logger.Debugf(ctx, "%s", msg)
	default:
		logger.Infof(ctx, "%s", msg)
	}
}

func (b *hlogBridge) logf(ctx context.Context, lv hlog.Level, format *string, v ...any) {
	if !b.enabled(lv) {
		return
	}
	var msg string
	if format != nil {
		if len(v) > 0 {
			msg = fmt.Sprintf(*format, v...)
		} else {
			msg = *format
		}
	} else {
		msg = fmt.Sprint(v...)
	}
	b.emit(ctx, lv, msg)
}

func (b *hlogBridge) Trace(v ...any)  { b.logf(context.Background(), hlog.LevelTrace, nil, v...) }
func (b *hlogBridge) Debug(v ...any)  { b.logf(context.Background(), hlog.LevelDebug, nil, v...) }
func (b *hlogBridge) Info(v ...any)   { b.logf(context.Background(), hlog.LevelInfo, nil, v...) }
func (b *hlogBridge) Notice(v ...any) { b.logf(context.Background(), hlog.LevelNotice, nil, v...) }
func (b *hlogBridge) Warn(v ...any)   { b.logf(context.Background(), hlog.LevelWarn, nil, v...) }
func (b *hlogBridge) Error(v ...any)  { b.logf(context.Background(), hlog.LevelError, nil, v...) }
func (b *hlogBridge) Fatal(v ...any)  { b.logf(context.Background(), hlog.LevelFatal, nil, v...) }

func (b *hlogBridge) Tracef(format string, v ...any) {
	b.logf(context.Background(), hlog.LevelTrace, &format, v...)
}
func (b *hlogBridge) Debugf(format string, v ...any) {
	b.logf(context.Background(), hlog.LevelDebug, &format, v...)
}
func (b *hlogBridge) Infof(format string, v ...any) {
	b.logf(context.Background(), hlog.LevelInfo, &format, v...)
}
func (b *hlogBridge) Noticef(format string, v ...any) {
	b.logf(context.Background(), hlog.LevelNotice, &format, v...)
}
func (b *hlogBridge) Warnf(format string, v ...any) {
	b.logf(context.Background(), hlog.LevelWarn, &format, v...)
}
func (b *hlogBridge) Errorf(format string, v ...any) {
	b.logf(context.Background(), hlog.LevelError, &format, v...)
}
func (b *hlogBridge) Fatalf(format string, v ...any) {
	b.logf(context.Background(), hlog.LevelFatal, &format, v...)
}

func (b *hlogBridge) CtxTracef(ctx context.Context, format string, v ...any) {
	b.logf(ctx, hlog.LevelTrace, &format, v...)
}
func (b *hlogBridge) CtxDebugf(ctx context.Context, format string, v ...any) {
	b.logf(ctx, hlog.LevelDebug, &format, v...)
}
func (b *hlogBridge) CtxInfof(ctx context.Context, format string, v ...any) {
	b.logf(ctx, hlog.LevelInfo, &format, v...)
}
func (b *hlogBridge) CtxNoticef(ctx context.Context, format string, v ...any) {
	b.logf(ctx, hlog.LevelNotice, &format, v...)
}
func (b *hlogBridge) CtxWarnf(ctx context.Context, format string, v ...any) {
	b.logf(ctx, hlog.LevelWarn, &format, v...)
}
func (b *hlogBridge) CtxErrorf(ctx context.Context, format string, v ...any) {
	b.logf(ctx, hlog.LevelError, &format, v...)
}
func (b *hlogBridge) CtxFatalf(ctx context.Context, format string, v ...any) {
	b.logf(ctx, hlog.LevelFatal, &format, v...)
}
