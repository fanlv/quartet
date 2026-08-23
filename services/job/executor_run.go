package job

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/types/model"
)

// SendMessage sends a message to an existing job session.
//
// Interactive messages vs a preserved (legacy loop) Resume:
//
//	Scenario                              | Status after | Resume
//	---                                   | ---          | ---
//	Stopped (paused) → send → msg done    | stopped      | preserved
//	Stopped (paused) → send → msg fails   | stopped      | preserved
//	Stopped (paused) → send → Stop msg    | stopped      | preserved
//	Completed → send → any outcome        | completed    | nil (unchanged)
//	Failed → send → any outcome           | failed       | preserved
//	Pending (never ran) → send → msg done | completed    | nil
//	Pending (never ran) → send → Stop msg | stopped      | nil
//
// Core rule: interactive messages never touch Resume, and any pre-existing
// terminal status (Completed/Failed/Stopped) is restored when the interactive
// run ends so an ad-hoc message never regresses it.
func (s *serviceImpl) SendMessage(ctx context.Context, jobID string, runner JobRunner, opts *SendMessageOptions) (SendMessageResult, error) {
	var result SendMessageResult
	if runner == nil {
		return result, fmt.Errorf("job runner is required")
	}
	asyncStarted := false
	if prepared, ok := runner.(PreparedExecutionReleaser); ok {
		defer func() {
			if !asyncStarted {
				prepared.ReleasePreparedExecution()
			}
		}()
	}
	// Validate before modifying any state to avoid leaving the job in Running
	// status if validation fails.
	if len(opts.getMessages()) == 0 {
		return result, ErrEmptyMessage
	}
	payloadHash, err := clientMessagePayloadHash(opts)
	if err != nil {
		return result, err
	}

	// Hold the persist shard across check→flip→persist so a concurrent
	// targeted mutator cannot interleave a stale snapshot over the fresh
	// Running state we set here.
	lock := s.persistLock(jobID)
	lock.Lock()
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()

	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return result, ErrJobNotFound
	}
	if job.Deleted {
		s.mu.Unlock()
		return result, ErrJobDeleted
	}
	if opts.ClientMessageID != "" {
		if _, exists := job.CommandReceipts[opts.ClientMessageID]; exists {
			s.mu.Unlock()
			return result, fmt.Errorf("%w: %q was used for a slash command", ErrClientMessageIDConflict, opts.ClientMessageID)
		}
		if receipt, exists := job.ClientMessageReceipts[opts.ClientMessageID]; exists {
			if receipt.PayloadHash != payloadHash {
				s.mu.Unlock()
				return result, fmt.Errorf("%w: %q", ErrClientMessageIDConflict, opts.ClientMessageID)
			}
			if receipt.State != model.ClientMessageStateQueued {
				s.mu.Unlock()
				return duplicateSendMessageResult(opts.ClientMessageID, receipt), nil
			}
		}
	}
	if job.Status == model.JobStatusRunning {
		s.mu.Unlock()
		return result, ErrJobRunning
	}

	prevRunState := snapshotRunStateLocked(job)
	queuedClaim := false
	if opts.ClientMessageID != "" {
		if receipt, exists := job.ClientMessageReceipts[opts.ClientMessageID]; exists && receipt.State == model.ClientMessageStateQueued {
			queueIndex := firstWaitingQueueIndex(job.MessageQueue)
			if job.MessageQueuePaused || queueIndex < 0 || job.MessageQueue[queueIndex].ID != opts.ClientMessageID {
				s.mu.Unlock()
				return result, ErrMessageQueueBlocked
			}
			job.MessageQueue[queueIndex].State = model.QueuedMessageStateProcessing
			job.MessageQueue[queueIndex].UpdatedAt = s.nowMillis()
			job.MessageQueueVersion++
			queuedClaim = true
		}
	}
	// Don't clear Resume — preserve any legacy (loop) resume cursor.
	// Remember the prior status so that terminal states (Completed/Failed/
	// Stopped) are restored when the interactive run ends, instead of being
	// overwritten by the interactive run's outcome.
	priorStatus := job.Status
	priorResume := job.Resume
	job.Status = model.JobStatusRunning
	job.StartedAt = s.nowMillis()
	job.FinishedAt = 0
	job.LastRunOutcome = ""
	if opts.ClientMessageID != "" {
		if job.ClientMessageReceipts == nil {
			job.ClientMessageReceipts = make(map[string]model.ClientMessageReceipt)
		}
		acceptedAt := job.StartedAt
		if queuedReceipt, exists := job.ClientMessageReceipts[opts.ClientMessageID]; exists && queuedReceipt.AcceptedAt > 0 {
			acceptedAt = queuedReceipt.AcceptedAt
		}
		receipt := model.ClientMessageReceipt{
			State:       model.ClientMessageStateProcessing,
			PayloadHash: payloadHash,
			AcceptedAt:  acceptedAt,
		}
		job.ClientMessageReceipts[opts.ClientMessageID] = receipt
		job.ActiveClientMessageID = opts.ClientMessageID
		result = SendMessageResult{
			Disposition: SendMessageStarted,
			Receipt:     newMessageReceipt(opts.ClientMessageID, receipt),
		}
	} else {
		result.Disposition = SendMessageStarted
	}
	startCtx := lifecycleStartContext{
		action:      jobRunActionSendMessage,
		hasResume:   priorResume != nil,
		priorStatus: priorStatus,
		scheduleID:  job.ScheduleID,
	}
	// Register cancel/done before releasing s.mu so they exist the instant
	// Status=running is observable: observers read status under s.mu (Get) and
	// only then act on it — a Stop that sees running is guaranteed to find the
	// cancel entry. prepareRunResources takes only the cancel/done mutexes
	// (never s.mu), so calling it here cannot deadlock. It must still precede
	// saveJobWithRetryUnderPersistLock so the persist-window race fix stays
	// intact.
	res := s.prepareRunResources(job.ID, 0)
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(ctx, job, jobRunActionSendMessageStart); err != nil {
		s.abortRunResources(job.ID, res)
		s.restoreRunStateAfterPersistFailure(ctx, job, prevRunState, jobRunActionSendMessageStart, err)
		return SendMessageResult{}, err
	}
	// SendMessage reuses the existing buffer. If the job already reached a
	// terminal status, MarkTerminal disabled GC; flip it back so the
	// interactive run's events get reclaimed.
	s.bus.resumeGC(job.ID)
	// Stopped without Resume is a chat-style stop (no continuability) — let
	// the new send's outcome drive the next status (success → Completed,
	// failure → Failed). Only preserve Stopped when there's a Resume (a
	// paused legacy run).
	//
	// A graph job is the exception: its status is owned by the graph run
	// lifecycle (SetGraphRunState), so an interactive discussion turn in one of
	// its node sessions must be status-neutral — always remember the prior
	// status so the terminal path restores it instead of promoting the parked
	// run to Completed (which would desync job.status from the still-running /
	if job.Mode == model.JobModeGraph || shouldPreservePriorStatus(priorStatus, priorResume) {
		s.setInteractivePriorStatus(job.ID, priorStatus)
	}

	logLifecycleStart(ctx, job.ID, startCtx)
	lock.Unlock()
	locked = false

	safe.Go(ctx, func() {
		defer func() {
			s.cleanupDone(job.ID, res.done)
			if opts.FromMessageQueue {
				s.completeClaimedQueueItem(context.Background(), job.ID, opts.ClientMessageID)
				s.clearEmptyMessageQueuePause(context.Background(), job.ID)
				s.finishMessageQueueDispatch(job.ID)
			} else {
				s.clearEmptyMessageQueuePause(context.Background(), job.ID)
				s.requestMessageQueueDispatch(job.ID)
			}
		}()
		s.runInteractive(res.ctx, job, runner, opts, res.entry)
	})
	asyncStarted = true
	if queuedClaim {
		s.publishMessageQueueChanged(job.ID)
	}
	return result, nil
}

func clientMessagePayloadHash(opts *SendMessageOptions) (string, error) {
	if opts == nil || opts.ClientMessageID == "" {
		return "", nil
	}
	payload := struct {
		SessionID       string                     `json:"sessionId,omitempty"`
		Messages        []clientMessagePayloadPart `json:"messages"`
		AgentType       string                     `json:"agentType,omitempty"`
		ModelID         string                     `json:"modelId,omitempty"`
		ACPMode         string                     `json:"acpMode,omitempty"`
		ACPThoughtLevel string                     `json:"acpThoughtLevel,omitempty"`
	}{
		SessionID:       opts.IdempotencySessionID,
		AgentType:       opts.AgentType,
		ModelID:         opts.ModelID,
		ACPMode:         opts.ACPMode,
		ACPThoughtLevel: opts.ACPThoughtLevel,
	}
	for _, message := range opts.Messages {
		if message == nil {
			payload.Messages = append(payload.Messages, clientMessagePayloadPart{})
			continue
		}
		payload.Messages = append(payload.Messages, clientMessagePayloadPart{
			Role:                  message.Role,
			Content:               message.Content,
			MultiContent:          message.MultiContent,
			UserInputMultiContent: message.UserInputMultiContent,
		})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("hash clientMessageId payload: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type clientMessagePayloadPart struct {
	Role                  schema.RoleType           `json:"role"`
	Content               string                    `json:"content,omitempty"`
	MultiContent          []schema.ChatMessagePart  `json:"multiContent,omitempty"`
	UserInputMultiContent []schema.MessageInputPart `json:"userInputMultiContent,omitempty"`
}

func newMessageReceipt(clientMessageID string, receipt model.ClientMessageReceipt) MessageReceipt {
	return MessageReceipt{
		ClientMessageID: clientMessageID,
		State:           receipt.State,
		AcceptedAt:      receipt.AcceptedAt,
		FinishedAt:      receipt.FinishedAt,
	}
}

func duplicateSendMessageResult(clientMessageID string, receipt model.ClientMessageReceipt) SendMessageResult {
	return SendMessageResult{
		Disposition: SendMessageDuplicate,
		Receipt:     newMessageReceipt(clientMessageID, receipt),
	}
}

// runInteractive executes a single user-initiated message round against the
// job's existing session (or a freshly created one). It does not touch Resume,
// and the prior terminal status (if any) is restored by the deferred
// finish/stop/fail path.
func (s *serviceImpl) runInteractive(ctx context.Context, job *model.Job, runner JobRunner, opts *SendMessageOptions, cancelEntry *cancelEntry) {
	if prepared, ok := runner.(PreparedExecutionReleaser); ok {
		defer prepared.ReleasePreparedExecution()
	}
	defer s.clearCancel(job.ID, cancelEntry)
	defer func() {
		// Read Status under the lock — failJob / stopJob may have already
		// flipped it in this goroutine, and a concurrent handler-side
		// SendMessage that observes a terminal status could be
		// writing it back to Running on another goroutine. The lock pairs
		// with those writes for visibility and silences the race detector.
		s.mu.RLock()
		status := job.Status
		s.mu.RUnlock()
		if status != model.JobStatusRunning {
			return
		}
		if ctx.Err() != nil {
			s.stopJob(ctx, job)
			return
		}

		s.finishJob(ctx, job)
	}()
	defer s.recoverRunPanic(ctx, job, jobRunSourceInteractive)

	logger.Debugf(ctx, "[interactive] start: jobId=%s", job.ID)
	if preparer, ok := runner.(AcceptedMessagePreparer); ok {
		if err := preparer.PrepareAcceptedMessage(ctx, job.ID); err != nil {
			logger.Errorf(ctx, "[interactive] prepare accepted message failed: jobId=%s err=%v", job.ID, err)
			s.failJob(ctx, job, err.Error())
			return
		}
	}

	// Publish JOB_STARTED so other SSE subscribers (e.g. another tab watching
	// this job) see the run re-enter Running. SendMessage already persisted
	// Status=running + StartedAt before reaching here, so the §1.4 "state
	// before publish" contract holds.
	s.publishJobStarted(job)

	sessionID := opts.SessionID
	if sessionID == "" {
		sid, err := s.initAndAttachSession(ctx, job, runner, &model.SessionOverrides{
			AgentType:       opts.AgentType,
			AgentBinding:    opts.AgentBinding,
			ModelID:         opts.ModelID,
			ACPMode:         opts.ACPMode,
			ACPThoughtLevel: opts.ACPThoughtLevel,
		})
		if err != nil {
			logger.Errorf(ctx, "[interactive] init session failed: jobId=%s err=%v", job.ID, err)
			// Interactive runs never write Progress.Results — failJob persists
			// the error on Progress.LastError and publishes JOB_FAILED.
			s.failJob(ctx, job, err.Error())
			return
		}
		sessionID = sid
	}

	// Extract the user message text so executeRepeat can publish RUN_STARTED /
	// RunOutcome for the round.
	msg := opts.Messages[0].Content
	if msg == "" && len(opts.Messages[0].UserInputMultiContent) > 0 {
		for _, part := range opts.Messages[0].UserInputMultiContent {
			if part.Text != "" {
				msg = part.Text
				break
			}
		}
	}

	s.executeRepeat(ctx, job, runner, msg, sessionID, opts)
}

func (s *serviceImpl) publishJobStarted(job *model.Job) {
	s.Publish(job.ID, &model.JobStartedEvent{
		BaseEvent: model.BaseEvent{Type: model.EventTypeJobStarted, JobID: job.ID, Timestamp: job.StartedAt},
	})
}

func (s *serviceImpl) initAndAttachSession(ctx context.Context, job *model.Job, runner JobRunner, overrides *model.SessionOverrides) (string, error) {
	sid, err := runner.InitSession(ctx, job.ID, overrides)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	firstSession := len(job.SessionIDs) == 0
	job.SessionIDs = append(job.SessionIDs, sid)
	if firstSession && job.FirstModelID == "" && overrides != nil && overrides.ModelID != "" {
		job.FirstModelID = overrides.ModelID
	}
	s.mu.Unlock()
	if err := s.saveJobWithRetry(ctx, job, jobPersistActionAttachSession); err != nil {
		s.recordPersistWarning(ctx, job, jobPersistActionAttachSession, err)
	}
	return sid, nil
}

// ensureProgress guarantees job.Progress is non-nil so other code paths can
// dereference it without per-call nil guards.
//
// Callers must invoke this only at the boundary where a *model.Job enters
// s.jobs (load on startup, Create) — runtime paths assume the invariant.
func ensureProgress(job *model.Job) {
	if job == nil || job.Progress != nil {
		return
	}
	job.Progress = &model.JobProgress{}
}
