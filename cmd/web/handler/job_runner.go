package handler

import (
	"context"
	"fmt"
	"strconv"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/msgextra"
)

// jobRunnerImpl implements job.JobRunner using Handler's internal methods.
// It handles both interactive (single message) and loop (preconfigured) modes.
type jobRunnerImpl struct {
	h       *Handler
	workdir string
	wsID    string
}

var _ job.JobRunner = (*jobRunnerImpl)(nil)

// newJobRunner creates a jobRunnerImpl from a job's current fields.
func newJobRunner(h *Handler, j *model.Job) *jobRunnerImpl {
	return &jobRunnerImpl{
		h:       h,
		workdir: j.Workdir,
		wsID:    j.WorkspaceID,
	}
}

func (r *jobRunnerImpl) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	var agentType string
	var modelID string
	var acpMode string
	var acpThoughtLevel string
	if overrides != nil {
		agentType = overrides.AgentType
		modelID = overrides.ModelID
		acpMode = overrides.ACPMode
		acpThoughtLevel = overrides.ACPThoughtLevel
	}

	s, err := r.h.createSession(ctx, modelID, agentType, r.workdir, r.wsID, jobID)
	if err != nil {
		return "", err
	}
	if acpMode != "" || acpThoughtLevel != "" {
		ss, err := r.h.getOrCreateSessionService(r.wsID, jobID)
		if err == nil {
			if acpMode != "" {
				if err := ss.UpdateACPMode(s.ID, acpMode); err != nil {
					logger.Errorf(ctx, "[InitSession] save session ACP mode failed: %v, sessionId=%s", err, s.ID)
				}
			}
			if acpThoughtLevel != "" {
				if err := ss.UpdateACPThoughtLevel(s.ID, acpThoughtLevel); err != nil {
					logger.Errorf(ctx, "[InitSession] save session ACP thought_level failed: %v, sessionId=%s", err, s.ID)
				}
			}
		}
	}
	return s.ID, nil
}

func (r *jobRunnerImpl) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	s, _, ok := r.h.getSessionByID(sessionID)
	if !ok {
		if reloaded, rok := r.h.reloadSessionByID(sessionID); rok {
			s = reloaded
			ok = true
		}
	}
	if !ok {
		return errSessionNotFound(sessionID)
	}

	// Try to update session title from the first user message content
	if len(messages) > 0 && messages[0].Role == schema.User && messages[0].Content != "" {
		r.h.tryUpdateSessionTitleFromUserContent(ctx, s, messages[0].Content)
	}

	switch s.Type {
	case consts.AgentTypeEino:
		return r.h.runEinoInternal(ctx, s, messages, handler)
	default:
		return r.h.runACPInternal(ctx, s, messages, handler)
	}
}

// BeginShellSession mints a fresh session for a Graph Shell node and appends the
// executed script as the user message of a shell-output transcript, mirroring
// the legacy Loop shell persistence (services/job persistShellMessages) so the
// Chat session sidebar renders it identically. It is called when the node is
// enqueued (before the shell runs) so the session — and the script — surface
// immediately rather than only after a slow shell exits. The combined output is
// appended later by FinishShellSession. The session is for display only — it is
// never used as a §3 会话血缘 lineage parent. Returns the new session id.
func (r *jobRunnerImpl) BeginShellSession(ctx context.Context, jobID, script string, startedAt int64) (string, error) {
	// Shell sessions are plain transcripts with no live agent behind them; type
	// them as eino so history loads/renders through the standard session path.
	s, err := r.h.createSession(ctx, "", consts.AgentTypeEino, r.workdir, r.wsID, jobID)
	if err != nil {
		return "", err
	}
	repo, err := repository.NewChatContextRepo(r.wsID, jobID, s.ID)
	if err != nil {
		return "", fmt.Errorf("create shell chat context repo: %w", err)
	}
	userMsg := schema.UserMessage(script)
	userMsg.Extra = map[string]any{
		msgextra.KeyShellOutput: true,
		msgextra.KeyStartedAt:   startedAt,
	}
	if err := repo.AppendMessages(ctx, []*schema.Message{userMsg}); err != nil {
		return "", fmt.Errorf("append shell script message: %w", err)
	}
	if ss, err := r.h.getOrCreateSessionService(r.wsID, jobID); err == nil {
		if err := ss.Touch(s.ID); err != nil {
			logger.Warnf(ctx, "[BeginShellSession] touch session failed: %v, sessionId=%s", err, s.ID)
		}
	}
	return s.ID, nil
}

// FinishShellSession appends the combined stdout/stderr as the assistant message
// of a shell display session previously created by BeginShellSession, completing
// the transcript once the shell has exited.
func (r *jobRunnerImpl) FinishShellSession(ctx context.Context, jobID, sessionID, output string, startedAt, finishedAt int64) error {
	repo, err := repository.NewChatContextRepo(r.wsID, jobID, sessionID)
	if err != nil {
		return fmt.Errorf("open shell chat context repo: %w", err)
	}
	assistantMsg := schema.AssistantMessage(output, nil)
	assistantMsg.Extra = map[string]any{
		msgextra.KeyShellOutput: true,
		msgextra.KeyStartedAt:   startedAt,
		msgextra.KeyFinishedAt:  finishedAt,
	}
	if err := repo.AppendMessages(ctx, []*schema.Message{assistantMsg}); err != nil {
		return fmt.Errorf("append shell output message: %w", err)
	}
	if ss, err := r.h.getOrCreateSessionService(r.wsID, jobID); err == nil {
		if err := ss.Touch(sessionID); err != nil {
			logger.Warnf(ctx, "[FinishShellSession] touch session failed: %v, sessionId=%s", err, sessionID)
		}
	}
	return nil
}

func errSessionNotFound(sessionID string) error {
	return &sessionNotFoundError{sessionID: sessionID}
}

// SessionModelID resolves the bound model id for sessionID, going through the
// shared session-service lookup so an evicted entry is reloaded from disk
// before we give up.
func (r *jobRunnerImpl) SessionModelID(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	s, ok := r.h.lookupSession(sessionID)
	if !ok || s == nil {
		return ""
	}
	return s.ModelID
}

type sessionNotFoundError struct {
	sessionID string
}

func (e *sessionNotFoundError) Error() string {
	return "session not found: " + e.sessionID
}

// ResolveModelSnapshot resolves a string modelID to its current ModelInstance
// content for GraphRun snapshot capture. ok=false when the id is empty/invalid
// or no live model resolves — the graph service treats that as a degraded
// (best-effort) snapshot rather than a hard failure.
func (r *jobRunnerImpl) ResolveModelSnapshot(ctx context.Context, modelID string) (model.ModelInstance, bool) {
	if modelID == "" || r.h.modelConfig == nil {
		return model.ModelInstance{}, false
	}
	id, err := strconv.ParseInt(modelID, 10, 64)
	if err != nil {
		return model.ModelInstance{}, false
	}
	inst, err := r.h.modelConfig.GetModelByID(ctx, id)
	if err != nil || inst == nil {
		return model.ModelInstance{}, false
	}
	return *inst, true
}

// ResolveSystemPrompt captures the resolved (placeholder-expanded) system prompt
// at this instant for GraphRun snapshot capture.
func (r *jobRunnerImpl) ResolveSystemPrompt(ctx context.Context) (string, error) {
	if r.h.promptService == nil {
		return "", nil
	}
	return r.h.promptService.ResolvePrompt(ctx, consts.KeySystemPrompt)
}
