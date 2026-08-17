package handler

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/msgextra"
)

// jobRunnerImpl implements job.JobRunner using Handler's internal methods.
// It handles both interactive (single message) and loop (preconfigured) modes.
type jobRunnerImpl struct {
	h                        *Handler
	workdir                  string
	wsID                     string
	preparedExecutionOnce    sync.Once
	preparedExecutionRelease func()
}

// shellSessionType marks a display-only Shell node transcript session. It is a
// non-agent marker: RunIteration never runs against it and it never matches a
// live ACP agent selector on the frontend, so history still loads/renders
// through the standard session path.
const shellSessionType = "shell"

var _ job.JobRunner = (*jobRunnerImpl)(nil)

// newJobRunner creates a jobRunnerImpl from a job's current fields.
func newJobRunner(h *Handler, j *model.Job) *jobRunnerImpl {
	return &jobRunnerImpl{
		h:       h,
		workdir: j.Workdir,
		wsID:    j.WorkspaceID,
	}
}

func (r *jobRunnerImpl) holdPreparedExecution(release func()) {
	r.preparedExecutionRelease = release
}

func (r *jobRunnerImpl) ReleasePreparedExecution() {
	if r == nil || r.preparedExecutionRelease == nil {
		return
	}
	r.preparedExecutionOnce.Do(r.preparedExecutionRelease)
}

func (r *jobRunnerImpl) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	var agentType string
	var modelID string
	var acpMode string
	var acpThoughtLevel string
	var agentBinding *model.AgentRuntimeBinding
	if overrides != nil {
		agentType = overrides.AgentType
		agentBinding = overrides.AgentBinding
		modelID = overrides.ModelID
		acpMode = overrides.ACPMode
		acpThoughtLevel = overrides.ACPThoughtLevel
	}

	s, err := r.h.createSessionWithBinding(ctx, modelID, agentType, r.workdir, r.wsID, jobID, agentBinding)
	if err != nil {
		r.ReleasePreparedExecution()
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
	defer r.ReleasePreparedExecution()
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

	return r.h.runACPInternal(ctx, s, messages, handler)
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
	// them with a dedicated marker so history loads/renders through the standard
	// session path without matching any live ACP agent selector on the frontend.
	s, err := r.h.createSession(ctx, "", shellSessionType, r.workdir, r.wsID, jobID)
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

// RecordPromptUserMessage appends the rendered prompt of a Graph Prompt/评估
// node as the user message of its (already created) session, before the agent
// runs. Mirrors BeginShellSession's persistence path so the Chat sidebar shows
// the auto-sent prompt the instant the node starts rather than only after the
// agent subprocess warms up and starts replying. The message is tagged with
// KeyPrePersisted by the caller when forwarded to RunIteration so the agent's
// BeginRun skips re-appending it (chatctx.BeginRun). Best-effort like the shell
// recorder: a failure is surfaced to the caller, which logs and falls back to
// the agent's own in-Run persistence.
func (r *jobRunnerImpl) RecordPromptUserMessage(ctx context.Context, jobID, sessionID, content string, startedAt int64) error {
	repo, err := repository.NewChatContextRepo(r.wsID, jobID, sessionID)
	if err != nil {
		return fmt.Errorf("open prompt chat context repo: %w", err)
	}
	userMsg := schema.UserMessage(content)
	userMsg.Extra = map[string]any{
		msgextra.KeyStartedAt: startedAt,
	}
	if err := repo.AppendMessages(ctx, []*schema.Message{userMsg}); err != nil {
		return fmt.Errorf("append prompt user message: %w", err)
	}
	if ss, err := r.h.getOrCreateSessionService(r.wsID, jobID); err == nil {
		if err := ss.Touch(sessionID); err != nil {
			logger.Warnf(ctx, "[RecordPromptUserMessage] touch session failed: %v, sessionId=%s", err, sessionID)
		}
	}
	return nil
}

func errSessionNotFound(sessionID string) error {
	return &sessionNotFoundError{sessionID: sessionID}
}

// SessionLastAssistantMessage reads the latest assistant reply of a session for
// the Clarify node's「讨论结论」capture (§ 交互澄清结点). It scans the persisted
// transcript from the tail and returns the first non-empty assistant message.
// ok=false means the session has no assistant turn yet (a clarify node
// opened with no initial prompt that the user continued without a reply). The
// content is trimmed so a downstream {{...}} reference sees no leading/trailing
// whitespace.
func (r *jobRunnerImpl) SessionLastAssistantMessage(ctx context.Context, jobID, sessionID string) (string, bool, error) {
	if sessionID == "" {
		return "", false, nil
	}
	repo, err := repository.NewChatContextRepo(r.wsID, jobID, sessionID)
	if err != nil {
		return "", false, fmt.Errorf("open clarify chat context repo: %w", err)
	}
	msgs, err := repo.LoadAllMessages(ctx)
	if err != nil {
		return "", false, fmt.Errorf("load clarify session messages: %w", err)
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m == nil || m.Role != schema.Assistant {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		return content, true, nil
	}
	return "", false, nil
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

// ResolveModelSnapshot returns a display name for modelID to freeze into a
// GraphRun snapshot. The numeric eino model-config store is gone; a graph
// node's ModelID is now the ACP model identifier, which is already display-
// ready, so it is snapshotted as-is. ok=false only when modelID is empty — the
// graph service treats that as a degraded (best-effort) snapshot rather than a
// hard failure.
func (r *jobRunnerImpl) ResolveModelSnapshot(_ context.Context, modelID string) (string, bool) {
	if modelID == "" {
		return "", false
	}
	return modelID, true
}

func (r *jobRunnerImpl) ResolveAgentSnapshot(ctx context.Context, identifier string) (model.GraphAgentSnapshot, error) {
	snapshot, release, err := r.ResolveAgentSnapshotWithLease(ctx, identifier)
	if release != nil {
		release()
	}
	return snapshot, err
}

func (r *jobRunnerImpl) ResolveAgentSnapshotWithLease(
	ctx context.Context,
	identifier string,
) (model.GraphAgentSnapshot, func(), error) {
	resolved, found, err := r.h.agentCatalog.Resolve(ctx, identifier)
	if err != nil {
		return model.GraphAgentSnapshot{}, nil, fmt.Errorf("resolve Agent %q failed: %w", identifier, err)
	}
	if !found {
		return model.GraphAgentSnapshot{}, nil, fmt.Errorf("resolve Agent %q failed: Agent does not exist", identifier)
	}
	if resolved.Deprecated || resolved.Lifecycle != model.AgentLifecycleActive {
		return model.GraphAgentSnapshot{}, nil, fmt.Errorf(
			"AgentID %q is not available for a new GraphRun: deprecated=%t lifecycle=%s",
			resolved.AgentID,
			resolved.Deprecated,
			resolved.Lifecycle,
		)
	}
	release, acquired := r.h.agentExecutions.acquireExecution(resolved.AgentID)
	if !acquired {
		return model.GraphAgentSnapshot{}, nil, fmt.Errorf(
			"AgentID %q cannot be snapshotted for a new GraphRun: Agent deletion is in progress",
			resolved.AgentID,
		)
	}
	binding := model.AgentRuntimeBinding{
		AgentID:    resolved.AgentID,
		Revision:   resolved.Revision,
		RuntimeKey: resolved.RuntimeKey,
		Definition: resolved.Definition,
	}
	if err := r.h.ensureBindingAvailable(ctx, binding); err != nil {
		release()
		return model.GraphAgentSnapshot{}, nil, err
	}
	return model.GraphAgentSnapshot{
		AgentID:    resolved.AgentID,
		Revision:   resolved.Revision,
		RuntimeKey: resolved.RuntimeKey,
		Definition: resolved.Definition,
	}, release, nil
}

func (r *jobRunnerImpl) ValidateAgentBindingWithLease(
	ctx context.Context,
	binding model.AgentRuntimeBinding,
) (func(), error) {
	entry, found, err := r.h.agentCatalog.Find(ctx, binding.AgentID)
	if err != nil {
		return nil, fmt.Errorf(
			"validate AgentID %q revision %q for Graph execution failed: %w",
			binding.AgentID,
			binding.Revision,
			err,
		)
	}
	if !found {
		return nil, fmt.Errorf(
			"AgentID %q revision %q cannot execute Graph: Agent does not exist",
			binding.AgentID,
			binding.Revision,
		)
	}
	if entry.Source == model.AgentCatalogSourceBuiltin &&
		entry.Builtin != nil && entry.Builtin.Deprecated {
		return nil, fmt.Errorf(
			"AgentID %q revision %q cannot execute Graph: Agent is deprecated",
			binding.AgentID,
			binding.Revision,
		)
	}
	if entry.Source == model.AgentCatalogSourceCustom &&
		(entry.Custom == nil || entry.Custom.Lifecycle != model.AgentLifecycleActive) {
		lifecycle := model.AgentLifecycle("")
		if entry.Custom != nil {
			lifecycle = entry.Custom.Lifecycle
		}
		return nil, fmt.Errorf(
			"AgentID %q revision %q cannot execute Graph: lifecycle=%q",
			binding.AgentID,
			binding.Revision,
			lifecycle,
		)
	}
	release, acquired := r.h.agentExecutions.acquireExecution(binding.AgentID)
	if !acquired {
		return nil, fmt.Errorf(
			"AgentID %q revision %q cannot execute Graph: Agent deletion is in progress",
			binding.AgentID,
			binding.Revision,
		)
	}
	if err := r.h.ensureBindingAvailable(ctx, binding); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (r *jobRunnerImpl) ValidateSessionAgentWithLease(
	ctx context.Context,
	sessionID string,
) (func(), error) {
	session, ok := r.h.lookupSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("Graph session %q does not exist", sessionID)
	}
	var (
		binding model.AgentRuntimeBinding
		found   bool
		err     error
	)
	if session.AgentID != "" && session.AgentRevision != "" {
		binding, found, err = r.h.agentCatalog.ResolveBinding(
			ctx,
			session.AgentID,
			session.AgentRevision,
		)
	} else {
		binding, found, err = r.h.agentCatalog.ResolveLegacyBinding(ctx, session.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve Graph session %q Agent binding failed: %w", sessionID, err)
	}
	if !found {
		return nil, fmt.Errorf(
			"Graph session %q Agent reference %q does not exist",
			sessionID,
			firstNonEmptyString(session.AgentID, session.Type),
		)
	}
	return r.ValidateAgentBindingWithLease(ctx, binding)
}

func (r *jobRunnerImpl) ResolveLegacyAgentBinding(ctx context.Context, identifier string) (*model.AgentRuntimeBinding, error) {
	binding, found, err := r.h.agentCatalog.ResolveLegacyBinding(ctx, identifier)
	if err != nil {
		return nil, fmt.Errorf("resolve legacy Agent %q failed: %w", identifier, err)
	}
	if !found {
		return nil, fmt.Errorf("resolve legacy Agent %q failed: Agent does not exist", identifier)
	}
	return &binding, nil
}
