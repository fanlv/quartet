package job

import (
	"context"
	"fmt"
	"strings"

	"github.com/fanlv/quartet/pkg/logger"
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
//
// A chat round is different from a workflow End node in two ways that matter for
// notifications: the user may be sitting in front of the streaming output, and
// another message may already be waiting in the durable queue. The hook stays
// quiet in either case so it only announces that the whole current conversation
// batch has finished.

// hookSourceInteractive is the QUARTET_HOOK_SOURCE value for an interactive round
// end, letting one shared script tell chats apart from graph node hooks
// ("prompt" / "end").
const hookSourceInteractive = "interactive"

// endHookAssistantMaxRunes bounds $QUARTET_LAST_ASSISTANT. A whole reply can be
// far larger than a notification needs and the process environment is a limited
// buffer, so the reply is truncated. Error text is NOT truncated — a failure
// notification must carry the full cause.
const endHookAssistantMaxRunes = 8000

// EndHookPolicy is the global default end-hook configuration as it applies to an
// interactive round: the script plus whether a round that somebody is watching
// live should skip its notification.
type EndHookPolicy struct {
	// Script is the global default script body. Blank disables the hook.
	Script string
	// SkipWhenWatched suppresses the hook when the Job has a live on-screen
	// viewer (see viewer.go): the user is already looking at the output, so a
	// "task finished" notification is noise. Graph node hooks are unaffected.
	SkipWhenWatched bool
}

// SetEndHookPolicyProvider wires the global default end-hook policy getter.
// Called once at startup (handler wiring) before any run launches, so it needs no
// locking against runs.
func (s *serviceImpl) SetEndHookPolicyProvider(fn func() EndHookPolicy) {
	s.endHookPolicyFn = fn
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
//
// When the Job has a live on-screen viewer and the policy says so, the hook is
// skipped: the user is watching the output stream, so a notification is noise.
// It is also skipped while another durable queue item is waiting. The currently
// finishing item is still marked processing until runInteractive returns, so it
// is deliberately not counted as waiting; this lets the final queued round fire
// the hook. Each skip is logged at Info so "why didn't I get notified" remains
// diagnosable.
func (s *serviceImpl) fireEndHook(ctx context.Context, job *model.Job, round interactiveRound) {
	if s.endHookPolicyFn == nil {
		return
	}
	policy := s.endHookPolicyFn()
	if strings.TrimSpace(policy.Script) == "" {
		return
	}

	watchers := s.WatchedBy(job.ID)
	if policy.SkipWhenWatched && watchers > 0 {
		logger.Infof(ctx, "[hook] skipped (job is being watched live): source=%s jobId=%s sessionId=%s viewers=%d",
			hookSourceInteractive, job.ID, round.sessionID, watchers)
		return
	}

	s.mu.RLock()
	queuedMessages := 0
	for i := range job.MessageQueue {
		if job.MessageQueue[i].State != model.QueuedMessageStateProcessing {
			queuedMessages++
		}
	}
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
	if queuedMessages > 0 {
		logger.Infof(ctx, "[hook] skipped (job has queued messages): source=%s jobId=%s sessionId=%s queued=%d",
			hookSourceInteractive, job.ID, round.sessionID, queuedMessages)
		return
	}

	watched := "0"
	if watchers > 0 {
		watched = "1"
	}
	req := shellhook.Request{
		Script:  policy.Script,
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
			"QUARTET_JOB_WATCHED":    watched,
		},
		LogFields: fmt.Sprintf("source=%s jobId=%s sessionId=%s runOutcome=%s watched=%s",
			hookSourceInteractive, job.ID, round.sessionID, outcome, watched),
	}
	logCtx := context.WithoutCancel(ctx)
	safe.Go(logCtx, func() {
		shellhook.Run(logCtx, req)
	})
}
