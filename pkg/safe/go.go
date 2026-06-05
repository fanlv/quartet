package safe

import (
	"context"
	"runtime/debug"

	"github.com/fanlv/quartet/pkg/logger"
)

// Go launches fn in a new goroutine with panic recovery. The ctx is used
// solely for structured logging — if the goroutine panics, the log line
// carries the trace/session context so it can be correlated in observability
// tools.
func Go(ctx context.Context, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf(ctx, "[PANIC] goroutine panic: %v\n%s", r, debug.Stack())
			}
		}()
		fn()
	}()
}
