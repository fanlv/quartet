package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/strutil"
	"github.com/fanlv/quartet/services/command"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/msgextra"
	"github.com/google/uuid"
)

func (h *Handler) JobMessage(ctx context.Context, c *app.RequestContext) {
	// Capture the server-received timestamp BEFORE any synchronous work in
	// prepareJobSend. The user_input repo routes by this value; sampling it
	// after the slow path would occasionally flip the entry into the wrong
	// daily file near a midnight boundary.
	receivedAt := time.Now()

	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	j, ok := h.jobService.Get(jobID)
	if !ok {
		httputil.NotFound(c, "job not found")
		return
	}

	var req model.JobMessageRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}

	// Chat-page command branch (方向一-2): when the first message is a known
	// slash command, execute it through the shared command module and push
	// the result as a transient SSE event. The command text is NOT saved to
	// the Job's message history — it's a UI reflex, not part of the
	// conversation.
	//
	// Only KNOWN commands (matching command.IsKnown) are intercepted. Unknown
	// slash text — a typo like /hlep, or a path like /etc/hosts — falls through
	// to the normal message flow and reaches the Agent, matching the frontend's
	// isKnownCommand check and IM's dispatchAdminCommand.
	//
	// Exception: when the caller explicitly opts out via BypassCommand, the
	// text is always treated as a regular message. The Web home page uses
	// this so `/help` typed before a Job exists becomes the Job's first
	// message; home-page inputs are always treated as normal messages.
	//
	// Extra guard: the command fast path ignores attachments and extra
	// messages (dispatchJobCommand only consumes cmd + args). If the caller
	// sent images or multiple messages alongside a command, fall through to
	// the normal message flow so those payloads are not silently dropped
	// (would-be orphan uploads on the server, lost follow-up messages from
	// non-web clients).
	if !req.BypassCommand && len(req.Messages) == 1 && len(req.Messages[0].ImageUrls) == 0 {
		if command.IsKnown(req.Messages[0].Content) {
			if j.Status == model.JobStatusRunning && !command.IsReadOnly(req.Messages[0].Content) {
				httputil.Conflict(c, "the command has side effects and cannot run while the job is running; try again after the current message finishes")
				return
			}
			cmdName, args := command.Parse(req.Messages[0].Content)
			execute := func() *model.CommandSystemMessageEvent {
				return h.dispatchJobCommand(ctx, j, req.ClientMessageID, cmdName, args)
			}
			event, duplicate, err := h.jobService.ExecuteCommand(
				ctx, j.ID, req.ClientMessageID, req.Messages[0].Content, execute,
			)
			if err != nil {
				httputil.MapError(c, err, jobErrMappings)
				return
			}
			if !duplicate {
				// ExecuteCommand persisted the receipt before this event becomes
				// visible. If the response is lost, a retry replays the receipt
				// instead of publishing the action a second time.
				h.jobService.PublishTransient(j.ID, event)
			}
			// Return the rendered command result inline in the POST response in
			// addition to the transient SSE broadcast. The SSE path only reaches
			// readers connected at publish time, but an interactive job that has
			// finished a round tears its SSE connection down (terminal-state
			// cleanup), so the publish would otherwise land on no reader and the
			// user would see nothing. The inline copy makes delivery to the
			// caller deterministic; the transient publish still updates any OTHER
			// tabs watching the same job.
			status := "command_dispatched"
			if duplicate {
				status = "command_duplicate"
			}
			c.JSON(http.StatusOK, map[string]any{"code": 0, "status": status, "event": event})
			return
		}
	}

	queueService, supportsQueue := h.jobService.(job.MessageQueueService)
	if !supportsQueue || j.Mode != model.JobModeInteractive {
		h.sendJobMessageDirect(ctx, c, j, &req, receivedAt)
		return
	}
	if req.ClientMessageID != "" {
		idempotencyOpts := h.prepareIdempotencyOptions(&req)
		receipt, found, lookupErr := h.jobService.LookupMessage(j.ID, idempotencyOpts)
		if lookupErr != nil {
			httputil.MapError(c, lookupErr, jobErrMappings)
			return
		}
		if found {
			snapshot, queueErr := queueService.MessageQueue(j.ID)
			if queueErr != nil {
				httputil.MapError(c, queueErr, jobErrMappings)
				return
			}
			disposition := job.SubmitMessageDuplicate
			if receipt.State == model.ClientMessageStateDeleted {
				disposition = job.SubmitMessageDeleted
			}
			writeSubmitMessageResult(c, job.SubmitMessageResult{Disposition: disposition, Receipt: receipt, Queue: snapshot})
			return
		}
	}

	queued, err := h.prepareQueuedJobMessage(ctx, j, &req)
	if err != nil {
		mapPrepareJobSendError(c, err)
		return
	}
	if principal, ok := CurrentPrincipal(c); ok {
		queued.ActorID = principal.User.ID
	}
	if string(c.GetHeader("X-Quartet-Client")) == "ios" {
		queued.Source = "ios"
	}

	result, err := queueService.SubmitMessage(ctx, j.ID, queued)
	if err != nil {
		logger.Errorf(ctx, "[job] submit message failed: jobId=%s err=%v", j.ID, err)
		httputil.MapError(c, err, jobErrMappings)
		return
	}

	// The audit stream is append-only. SubmitMessage reports queued only for the
	// first durable acceptance, so retries never duplicate these rows.
	if result.Disposition == job.SubmitMessageQueued || result.Disposition == job.SubmitMessageStarted {
		h.appendWebUserInputs(ctx, j, &req, receivedAt)
	}

	writeSubmitMessageResult(c, result)
}

func (h *Handler) sendJobMessageDirect(ctx context.Context, c *app.RequestContext, j *model.Job, req *model.JobMessageRequest, receivedAt time.Time) {
	if req.ClientMessageID != "" {
		idempotencyOpts := h.prepareIdempotencyOptions(req)
		receipt, found, err := h.jobService.LookupMessage(j.ID, idempotencyOpts)
		if err != nil {
			httputil.MapError(c, err, jobErrMappings)
			return
		}
		if found {
			writeJobMessageResult(c, job.SendMessageResult{Disposition: job.SendMessageDuplicate, Receipt: receipt})
			return
		}
	}
	runner, opts, err := h.prepareJobSend(ctx, j, req)
	if err != nil {
		mapPrepareJobSendError(c, err)
		return
	}
	result, err := h.jobService.SendMessage(ctx, j.ID, runner, opts)
	if err != nil {
		httputil.MapError(c, err, jobErrMappings)
		return
	}
	if result.Started() {
		h.appendWebUserInputs(ctx, j, req, receivedAt)
	}
	writeJobMessageResult(c, result)
}

func (h *Handler) appendWebUserInputs(ctx context.Context, j *model.Job, req *model.JobMessageRequest, receivedAt time.Time) {
	if h.userInputRepo == nil {
		return
	}
	for idx, m := range req.Messages {
		if m.Content == "" && len(m.ImageUrls) == 0 {
			continue
		}
		if command.IsKnown(m.Content) {
			continue
		}
		msgID := m.ID
		if msgID == "" {
			if idx == 0 && req.ClientMessageID != "" {
				msgID = req.ClientMessageID
			} else {
				msgID = uuid.NewString()
			}
		}
		input := model.NewWebUserInput(receivedAt, msgID, j.ID, j.WorkspaceID, m.Content, m.ImageUrls)
		if err := h.userInputRepo.Append(ctx, input); err != nil {
			logger.Errorf(ctx, "[user_input] append web failed: jobId=%s err=%v", j.ID, err)
		}
	}
}

func writeSubmitMessageResult(c *app.RequestContext, result job.SubmitMessageResult) {
	response := map[string]any{
		"code": 0, "status": result.Disposition, "queue": result.Queue,
	}
	if result.Receipt.ClientMessageID != "" {
		response["clientMessageId"] = result.Receipt.ClientMessageID
		response["messageState"] = result.Receipt.State
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) prepareQueuedJobMessage(ctx context.Context, j *model.Job, req *model.JobMessageRequest) (model.QueuedJobMessage, error) {
	if j.Deleted {
		return model.QueuedJobMessage{}, job.ErrJobDeleted
	}
	if j.Mode != model.JobModeInteractive {
		return model.QueuedJobMessage{}, job.ErrJobNotRunnable
	}
	if req.ClientMessageID == "" {
		return model.QueuedJobMessage{}, fmt.Errorf("clientMessageId is required")
	}
	if len(req.Messages) == 0 {
		return model.QueuedJobMessage{}, job.ErrEmptyMessage
	}
	agentID, revision, err := h.resolveInteractiveExecutionTarget(ctx, j, req)
	if err != nil {
		return model.QueuedJobMessage{}, err
	}
	return model.QueuedJobMessage{
		ID: req.ClientMessageID, Messages: req.Messages, SessionID: req.SessionID,
		AgentType: req.AgentType, AgentID: agentID, AgentRevision: revision,
		ModelID: req.ModelID, ACPMode: req.ACPMode, ACPThoughtLevel: req.ACPThoughtLevel,
		BypassCommand: req.BypassCommand, Source: "web", CreatedAt: time.Now().UnixMilli(),
	}, nil
}

func writeJobMessageResult(c *app.RequestContext, result job.SendMessageResult) {
	status := string(result.Disposition)
	if status == "" {
		status = string(job.SendMessageStarted)
	}
	response := map[string]any{
		"code":   0,
		"status": status,
	}
	if result.Receipt.ClientMessageID != "" {
		response["clientMessageId"] = result.Receipt.ClientMessageID
		response["messageState"] = result.Receipt.State
	}
	c.JSON(http.StatusOK, response)
}

// mapPrepareJobSendError maps a prepareJobSend failure to an HTTP response.
// Job-state sentinel rejections (running / deleted) reuse the lifecycle
// mappings (409 / 404) so clients can distinguish "retry later" from a
// malformed request; validation failures stay 400.
func mapPrepareJobSendError(c *app.RequestContext, err error) {
	if errors.Is(err, job.ErrJobRunning) || errors.Is(err, job.ErrJobDeleted) {
		httputil.MapError(c, err, jobErrMappings)
		return
	}
	httputil.BadRequest(c, err.Error())
}

// dispatchJobCommand runs a slash command in the context of an existing Job,
// pushes the result as a transient COMMAND_SYSTEM_MESSAGE SSE event, and
// returns the same event so the HTTP handler can also deliver it inline in the
// POST response. Command feedback is transient: it is NOT added to the per-job
// SSE event buffer, so a subsequent refresh / reconnect will not re-display the
// bubble or re-fire the embedded action (which would silently teleport the user
// to another workspace / Job on every revisit).
//
// Unknown commands are handled here too — the shared command module's
// Execute returns ok=false, and we emit a "未知命令" system message so Web
// behaves the same as IM (neither side forwards the text to the Agent).
func (h *Handler) dispatchJobCommand(ctx context.Context, j *model.Job, clientMessageID, cmd, args string) *model.CommandSystemMessageEvent {
	ec := &command.ExecCtx{
		Ctx:                ctx,
		WorkspaceService:   h.workspaceService,
		JobService:         h.jobService,
		CurrentWorkspaceID: j.WorkspaceID,
		CurrentJobID:       j.ID,
	}
	result, ok := command.Execute(cmd, args, ec)
	event := &model.CommandSystemMessageEvent{
		BaseEvent: model.BaseEvent{
			Type:      model.EventTypeCommandSystemMessage,
			JobID:     j.ID,
			Timestamp: time.Now().UnixMilli(),
		},
		ClientMessageID: clientMessageID,
	}
	if !ok {
		event.Command = cmd
		event.Text = fmt.Sprintf("未知命令: %s\n输入 /help 查看可用命令", cmd)
		event.Present = string(command.PresentInline)
		return event
	}
	event.Command = command.ResolveName(cmd)
	event.Text = result.Message.Text
	event.Present = string(result.Message.Present)
	// Only attach Action for commands that request a side effect, so
	// display-only commands (/help, /status) don't ship an empty action
	// object that would needlessly trigger the frontend's command-action
	// dispatcher checks.
	if result.Action.Type != "" {
		event.Action = &model.CommandAction{
			Type:            string(result.Action.Type),
			WorkspaceID:     result.Action.WorkspaceID,
			JobID:           result.Action.JobID,
			ClientMessageID: commandActionClientMessageID(j.ID, clientMessageID),
		}
	}
	return event
}

func commandActionClientMessageID(jobID, clientMessageID string) string {
	if clientMessageID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(jobID + "\x00" + clientMessageID))
	return "command-action-" + hex.EncodeToString(sum[:])
}

// prepareJobSend runs the full pre-SendMessage flow shared by the HTTP
// JobMessage handler and the IM gateway: validates the request, builds
// SendMessageOptions and returns a runner+opts ready to hand to
// jobService.SendMessage. Metadata writes are attached to the runner and run
// only after SendMessage durably accepts this request.
//
// The job-state gate is a cheap early rejection only. SendMessage's locked
// check is authoritative; deferring metadata writes to PrepareAcceptedMessage
// closes the residual check/claim TOCTOU window.
func (h *Handler) prepareJobSend(ctx context.Context, j *model.Job, req *model.JobMessageRequest) (job.JobRunner, *job.SendMessageOptions, error) {
	if j.Deleted {
		return nil, nil, job.ErrJobDeleted
	}
	if j.Status == model.JobStatusRunning {
		return nil, nil, job.ErrJobRunning
	}

	agentID, revision, err := h.resolveInteractiveExecutionTarget(ctx, j, req)
	if err != nil {
		return nil, nil, err
	}
	runner := newJobRunner(h, j)
	if agentID != "" {
		releaseExecution, acquired := h.agentExecutions.acquireExecution(agentID)
		if !acquired {
			return nil, nil, fmt.Errorf(
				"AgentID %q revision %q cannot start an interactive run: Agent deletion is in progress",
				agentID,
				revision,
			)
		}
		runner.holdPreparedExecution(releaseExecution)
	}

	opts, err := h.prepareJobMessage(j, req)
	if err != nil {
		runner.ReleasePreparedExecution()
		return nil, nil, err
	}

	runner.prepareAccepted = func(acceptedCtx context.Context, jobID string) error {
		if opts.SessionID != "" {
			h.maybeUpdateSessionModel(opts.SessionID, req.ModelID)
			h.maybeUpdateSessionACPFields(opts.SessionID, req.ACPMode, req.ACPThoughtLevel)
		}
		pendingTitle, userMessage, needSave, shouldRefine := h.planJobTitleUpdate(j, req)
		if needSave {
			if err := h.jobService.UpdateTitle(jobID, pendingTitle); err != nil {
				logger.Errorf(acceptedCtx, "[job] update title failed: jobId=%s err=%v", jobID, err)
			}
		}
		if shouldRefine {
			h.asyncUpdateJobTitle(acceptedCtx, jobID, userMessage)
		}
		return nil
	}

	return runner, opts, nil
}

func (h *Handler) resolveInteractiveExecutionTarget(
	ctx context.Context,
	j *model.Job,
	req *model.JobMessageRequest,
) (string, string, error) {
	sessionID, err := h.resolveSessionID(j, req.SessionID)
	if err != nil {
		return "", "", err
	}
	if sessionID != "" {
		session, ok := h.lookupSession(sessionID)
		if !ok {
			return "", "", fmt.Errorf("session %s was not found", sessionID)
		}
		if req.AgentType != "" && !h.sameAgentReference(ctx, session, req.AgentType) {
			if req.SessionID != "" {
				return "", "", fmt.Errorf(
					"session %s agentType=%s does not match request agentType=%s",
					sessionID,
					session.Type,
					req.AgentType,
				)
			}
			sessionID = ""
		} else {
			var (
				binding    model.AgentRuntimeBinding
				found      bool
				resolveErr error
			)
			if session.AgentID != "" && session.AgentRevision != "" {
				binding, found, resolveErr = h.agentCatalog.ResolveBinding(
					ctx,
					session.AgentID,
					session.AgentRevision,
				)
			} else {
				binding, found, resolveErr = h.agentCatalog.ResolveLegacyBinding(ctx, session.Type)
			}
			if resolveErr != nil {
				return "", "", resolveErr
			}
			if !found {
				return "", "", fmt.Errorf(
					"session %s Agent reference %q does not exist",
					sessionID,
					firstNonEmptyString(session.AgentID, session.Type),
				)
			}
			if err := h.validateInteractiveExecutionAgent(ctx, binding.AgentID, binding.Revision); err != nil {
				return "", "", err
			}
			return binding.AgentID, binding.Revision, nil
		}
	}
	if sessionID == "" && req.AgentType == "" {
		return "", "", nil
	}
	resolved, found, resolveErr := h.agentCatalog.Resolve(ctx, req.AgentType)
	if resolveErr != nil {
		return "", "", fmt.Errorf("resolve Agent %q before interactive run failed: %w", req.AgentType, resolveErr)
	}
	if !found {
		return "", "", fmt.Errorf("resolve Agent %q before interactive run failed: Agent does not exist", req.AgentType)
	}
	if resolved.Deprecated || resolved.Lifecycle != model.AgentLifecycleActive {
		return "", "", fmt.Errorf(
			"AgentID %q revision %q cannot start an interactive run: deprecated=%t lifecycle=%q",
			resolved.AgentID,
			resolved.Revision,
			resolved.Deprecated,
			resolved.Lifecycle,
		)
	}
	return resolved.AgentID, resolved.Revision, nil
}

func (h *Handler) validateInteractiveExecutionAgent(ctx context.Context, agentID, revision string) error {
	entry, found, err := h.agentCatalog.Find(ctx, agentID)
	if err != nil {
		return fmt.Errorf(
			"validate AgentID %q revision %q before interactive run failed: %w",
			agentID,
			revision,
			err,
		)
	}
	if !found {
		return fmt.Errorf(
			"AgentID %q revision %q cannot start an interactive run: Agent does not exist",
			agentID,
			revision,
		)
	}
	if entry.Source == model.AgentCatalogSourceBuiltin &&
		entry.Builtin != nil && entry.Builtin.Deprecated {
		return fmt.Errorf(
			"AgentID %q revision %q cannot start an interactive run: Agent is deprecated",
			agentID,
			revision,
		)
	}
	if entry.Source == model.AgentCatalogSourceCustom &&
		(entry.Custom == nil || entry.Custom.Lifecycle != model.AgentLifecycleActive) {
		lifecycle := model.AgentLifecycle("")
		if entry.Custom != nil {
			lifecycle = entry.Custom.Lifecycle
		}
		return fmt.Errorf(
			"AgentID %q revision %q cannot start an interactive run: lifecycle=%q",
			agentID,
			revision,
			lifecycle,
		)
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// planJobTitleUpdate computes a pending title for the job if needed and
// schedules an async LLM-based refinement.
func (h *Handler) planJobTitleUpdate(j *model.Job, req *model.JobMessageRequest) (pendingTitle, userMessage string, needSave, shouldRefine bool) {
	isDefaultTitle := false
	if (j.Title == "" || j.Title == consts.DefaultJobTitle) && len(req.Messages) > 0 && req.Messages[0].Content != "" {
		pendingTitle = strutil.TruncateRunes(req.Messages[0].Content, 30)
		needSave = true
		isDefaultTitle = true
	}

	if isDefaultTitle || j.Title == consts.DefaultJobTitle || j.Title == "" {
		var parts []string
		for _, m := range req.Messages {
			if m.Content != "" {
				parts = append(parts, replaceJobTitleVariables(m.Content, j.LoopConfig))
			}
		}
		userMessage = strings.Join(parts, "\n")
		shouldRefine = true
	}
	return pendingTitle, userMessage, needSave, shouldRefine
}

// prepareJobMessage validates a JobMessageRequest and builds SendMessageOptions
// for the given job.
func (h *Handler) prepareJobMessage(j *model.Job, req *model.JobMessageRequest) (*job.SendMessageOptions, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages is required")
	}

	opts, err := h.prepareInteractiveRun(j, req)
	if err != nil {
		return nil, err
	}

	return opts, nil
}

// prepareInteractiveRun handles interactive mode: build messages, resolve session, update model.
//
// All agents (ACP CLIs, including eino-cli) take image inputs as an
// `![image](<abs path>)` text tag prepended to the message content, so the
// image URLs are always preserved verbatim for downstream records (user_input,
// etc.) — nothing is dropped here.
func (h *Handler) prepareInteractiveRun(j *model.Job, req *model.JobMessageRequest) (*job.SendMessageOptions, error) {
	opts := h.prepareIdempotencyOptions(req)

	// Stamp user messages with a receive timestamp and a stable msg_id so
	// that history reload can display timestamps and produce stable IDs
	// (avoiding duplicate bubbles when a reload re-indexes messages).
	nowMs := time.Now().UnixMilli()
	for i, msg := range opts.Messages {
		if msg.Extra == nil {
			msg.Extra = map[string]any{}
		}
		if _, exists := msg.Extra[msgextra.KeyStartedAt]; !exists {
			msg.Extra[msgextra.KeyStartedAt] = nowMs
		}
		// Assign a stable msg_id: use ClientMessageID for the first message
		// (matches the frontend's optimistic ID), generate UUID for the rest.
		if _, exists := msg.Extra[msgextra.KeyMsgID]; !exists {
			if i == 0 && req.ClientMessageID != "" {
				msg.Extra[msgextra.KeyMsgID] = req.ClientMessageID
			} else {
				msg.Extra[msgextra.KeyMsgID] = uuid.NewString()
			}
		}
	}

	sessionID, err := h.resolveSessionID(j, req.SessionID)
	if err != nil {
		return nil, err
	}

	// Agent type is effectively a session-level choice: the run ultimately uses
	// session.Type (see jobRunnerImpl.RunIteration -> runACPInternal).
	//
	// If the caller did not explicitly pin a sessionId and the job has an existing
	// session with a different type, prefer creating a new session so the new
	// agentType can take effect. This avoids reusing a cached ACPAgent keyed by
	// sessionID with a mismatched underlying process.
	if sessionID != "" && req.AgentType != "" {
		s, ok := h.lookupSession(sessionID)
		if ok && s.Type != "" && !h.sameAgentReference(context.Background(), s, req.AgentType) {
			if req.SessionID != "" {
				return nil, fmt.Errorf("session %s agentType=%s does not match request agentType=%s", sessionID, s.Type, req.AgentType)
			}
			sessionID = ""
		}
	}

	if sessionID != "" {
		opts.SessionID = sessionID
	}
	return opts, nil
}

// prepareIdempotencyOptions builds the stable portion of SendMessageOptions.
// It deliberately excludes server receive timestamps and generated msg_id
// values so an otherwise identical HTTP retry hashes to the same payload.
func (h *Handler) prepareIdempotencyOptions(req *model.JobMessageRequest) *job.SendMessageOptions {
	msgs := make([]*schema.Message, 0, len(req.Messages))
	for i := range req.Messages {
		m := &req.Messages[i]
		content := m.Content
		if len(m.ImageUrls) > 0 {
			var prefix string
			for _, u := range m.ImageUrls {
				prefix += fmt.Sprintf("![image](%s)\n", u)
			}
			content = prefix + content
		}
		msgs = append(msgs, schema.UserMessage(content))
	}
	return &job.SendMessageOptions{
		IdempotencySessionID: req.SessionID,
		ClientMessageID:      req.ClientMessageID,
		Messages:             msgs,
		AgentType:            req.AgentType,
		ModelID:              req.ModelID,
		ACPMode:              req.ACPMode,
		ACPThoughtLevel:      req.ACPThoughtLevel,
	}
}

func (h *Handler) sameAgentReference(ctx context.Context, session *model.Session, requested string) bool {
	if session == nil {
		return false
	}
	if session.AgentID != "" {
		resolved, found, err := h.agentCatalog.Resolve(ctx, requested)
		return err == nil && found && resolved.AgentID == session.AgentID
	}
	existing, existingFound, existingErr := h.agentCatalog.Resolve(ctx, session.Type)
	requestedAgent, requestedFound, requestedErr := h.agentCatalog.Resolve(ctx, requested)
	return existingErr == nil && requestedErr == nil &&
		existingFound && requestedFound &&
		existing.AgentID == requestedAgent.AgentID
}

// resolveSessionID returns the session ID from the request (after validating it
// belongs to this job), or falls back to the job's last session.  An error is
// returned when the caller supplies a session ID that is not associated with the job.
//
// A requested session is accepted if it appears in either SessionIDs
// (loop/interactive) or GraphSessionIDs (graph node sessions) — the latter lets
// a user keep chatting in a finished graph node's session after the run stops.
// The empty-request fallback deliberately reads only SessionIDs[last] so graph's
// non-linear, concurrent node sessions never hijack the default target; a graph
// client always sends an explicit sessionId.
func (h *Handler) resolveSessionID(j *model.Job, reqSessionID string) (string, error) {
	if reqSessionID != "" {
		if sessionBelongsToJob(j, reqSessionID) {
			return reqSessionID, nil
		}
		return "", fmt.Errorf("session %s does not belong to job %s", reqSessionID, j.ID)
	}
	if len(j.SessionIDs) > 0 {
		return j.SessionIDs[len(j.SessionIDs)-1], nil
	}
	return "", nil
}

// maybeUpdateSessionModel updates the session's model only when modelID differs.
func (h *Handler) maybeUpdateSessionModel(sessionID string, modelID string) {
	if modelID == "" {
		return
	}
	ss, ok := h.lookupSessionService(sessionID)
	if !ok {
		logger.Error("[session] update model skipped, session not found after reload: sessionId=%s", sessionID)
		return
	}
	if err := ss.UpdateModelID(sessionID, modelID); err != nil {
		logger.Error("[session] save model failed: sessionId=%s err=%v", sessionID, err)
	}
}

func (h *Handler) maybeUpdateSessionACPFields(sessionID, acpMode, acpThoughtLevel string) {
	if acpMode == "" && acpThoughtLevel == "" {
		return
	}
	ss, ok := h.lookupSessionService(sessionID)
	if !ok {
		logger.Error("[session] update ACP fields skipped, session not found after reload: sessionId=%s", sessionID)
		return
	}
	if acpMode != "" {
		if err := ss.UpdateACPMode(sessionID, acpMode); err != nil {
			logger.Error("[session] save ACP mode failed: sessionId=%s err=%v", sessionID, err)
		}
	}
	if acpThoughtLevel != "" {
		if err := ss.UpdateACPThoughtLevel(sessionID, acpThoughtLevel); err != nil {
			logger.Error("[session] save ACP thought_level failed: sessionId=%s err=%v", sessionID, err)
		}
	}
}
