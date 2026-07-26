package job

import (
	"context"
	"runtime/debug"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// recoverRunPanic must be registered directly with defer so recover can observe
// the panic. It runs before the normal lifecycle defer in runInteractive.
func (s *serviceImpl) recoverRunPanic(ctx context.Context, job *model.Job, source string) {
	if r := recover(); r != nil {
		panicErr := newRunPanicError(r)
		logger.Errorf(ctx, "[%s] panic: jobId=%s err=%v\n%s", source, job.ID, r, string(debug.Stack()))
		s.closePanicRoundIfOpen(job, panicErr)
		s.failJob(ctx, job, panicErr.Error())
	}
}
