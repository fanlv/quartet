package job

import (
	"context"
	"fmt"
	"strings"

	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/pkg/shellhook"
	"github.com/fanlv/quartet/pkg/strutil"
	"github.com/fanlv/quartet/types/model"
)

// End hook (§ 结束 Hook): the same global default script a graph workflow runs at
// its End node also runs after every interactive round terminates
// (completed / failed / stopped), so "task finished" notifications work for chats
// as well as workflows. It is a pure side effect — see pkg/shellhook: the output
// is ignored and a failure only logs, never touching the Job's state.

// hookSourceInteractive is the QUARTET_HOOK_SOURCE value for an interactive round
// end, letting one shared script tell chats apart from graph node hooks
// ("prompt" / "end").
const hookSourceInteractive = "interactive"

// endHookAssistantMaxRunes bounds $QUARTET_LAST_ASSISTANT. A whole reply can be
// far larger than a notification needs and the process environment is a limited
// buffer, so the reply is truncated. Error text is NOT truncated — a failure
// notification must carry the full cause.
const endHookAssistantMaxRunes = 8000

// SetEndHookScriptProvider wires the global default end-hook script getter.
// Called once at startup (handler wiring) before any run launches, so it needs no
// locking against runs.
func (s *serviceImpl) SetEndHookScriptProvider(fn func() string) {
	s.endHookScriptFn = fn
}

// interactiveRound carries what an interactive round's end hook reports beyond
// the Job itself. It is filled in as the round progresses and read by the
// deferred hook trigger, so a round that dies before reaching the agent still
// fires the hook with whatever is known.
type interactiveRound struct {
	sessionID     string
	assistantText string
}

// fireEndHook launches the global default end hook for a just-terminated
// interactive round. It snapshots the Job under s.mu before spawning so the hook
// goroutine never touches shared state, and detaches the context so a run-level
// cancel (user Stop) cannot kill a side effect for a round that already ended.
// Asynchronous on purpose: the caller's goroutine goes on to release the run and
// dispatch the next queued message, which must not wait on a user script.
func (s *serviceImpl) fireEndHook(ctx context.Context, job *model.Job, round interactiveRound) {
	if s.endHookScriptFn == nil {
		return
	}
	script := s.endHookScriptFn()
	if strings.TrimSpace(script) == "" {
		return
	}

	s.mu.RLock()
	title := job.Title
	mode := job.Mode
	status := job.Status
	outcome := job.LastRunOutcome
	workdir := job.Workdir
	errMessage := ""
	if outcome == model.RunOutcomeFailed && job.Progress != nil {
		errMessage = job.Progress.LastError
	}
	s.mu.RUnlock()

	req := shellhook.Request{
		Script:  script,
		Workdir: workdir,
		Context: map[string]string{
			"QUARTET_HOOK_SOURCE":    hookSourceInteractive,
			"QUARTET_JOB_TITLE":      title,
			"QUARTET_JOB_ID":         job.ID,
			"QUARTET_JOB_MODE":       string(mode),
			"QUARTET_JOB_STATUS":     string(status),
			"QUARTET_RUN_OUTCOME":    string(outcome),
			"QUARTET_SESSION_ID":     round.sessionID,
			"QUARTET_LAST_ASSISTANT": strutil.TruncateRunesWithEllipsis(round.assistantText, endHookAssistantMaxRunes),
			"QUARTET_ERROR_MESSAGE":  errMessage,
		},
		LogFields: fmt.Sprintf("source=%s jobId=%s sessionId=%s runOutcome=%s",
			hookSourceInteractive, job.ID, round.sessionID, outcome),
	}
	logCtx := context.WithoutCancel(ctx)
	safe.Go(logCtx, func() {
		shellhook.Run(logCtx, req)
	})
}
