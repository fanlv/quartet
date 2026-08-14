package handler

import (
	"context"
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
			cmdName, args := command.Parse(req.Messages[0].Content)
			event := h.dispatchJobCommand(ctx, j, cmdName, args)
			// Return the rendered command result inline in the POST response in
			// addition to the transient SSE broadcast. The SSE path only reaches
			// readers connected at publish time, but an interactive job that has
			// finished a round tears its SSE connection down (terminal-state
			// cleanup), so the publish would otherwise land on no reader and the
			// user would see nothing. The inline copy makes delivery to the
			// caller deterministic; the transient publish still updates any OTHER
			// tabs watching the same job.
			c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "command_dispatched", "event": event})
			return
		}
	}

	// "真实用户输入" 窄口径流：命令已在上面的快速路径 return，此处按 messages 数组
	// 顺序遍历落盘，每条非空消息一条 JSONL 条目（文档 §3.6 "命令一律不落"：即便
	// 走到这里，也对每条消息再做一次命令判定，命中即跳过；这样覆盖
	// BypassCommand、多消息、带图命令等快速路径未拦截的场景）。
	// 判定口径与 IM 侧 dispatchAdminCommand 保持一致：只有已知命令（command.IsKnown）
	// 才跳过落盘，未知 slash 文本（例如拼错的 /hlep 或 /etc/hosts 这类路径）按真实
	// 用户消息落盘并转发给 Agent。
	// 落盘发生在 prepareJobSend 成功之后，避免参数错误时留脏数据（文档 §3.5）。
	runner, opts, err := h.prepareJobSend(ctx, j, &req)
	if err != nil {
		mapPrepareJobSendError(c, err)
		return
	}

	if h.userInputRepo != nil {
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

	if err := h.jobService.SendMessage(ctx, j.ID, runner, opts); err != nil {
		logger.Errorf(ctx, "[job] send message failed: jobId=%s err=%v", j.ID, err)
		httputil.MapError(c, err, jobErrMappings)
		return
	}

	c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "started"})
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
func (h *Handler) dispatchJobCommand(ctx context.Context, j *model.Job, cmd, args string) *model.CommandSystemMessageEvent {
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
	}
	if !ok {
		event.Command = cmd
		event.Text = fmt.Sprintf("未知命令: %s\n输入 /help 查看可用命令", cmd)
		event.Present = string(command.PresentInline)
		h.jobService.PublishTransient(j.ID, event)
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
			Type:        string(result.Action.Type),
			WorkspaceID: result.Action.WorkspaceID,
			JobID:       result.Action.JobID,
		}
	}
	h.jobService.PublishTransient(j.ID, event)
	return event
}

// prepareJobSend runs the full pre-SendMessage flow shared by the HTTP
// JobMessage handler and the IM gateway: validates the request, builds
// SendMessageOptions, applies any pending title update to the job (without
// waiting for SendMessage to succeed), and returns a runner+opts ready to
// hand to jobService.SendMessage.
//
// The job-state gate runs BEFORE any metadata side effects (title / session
// model / ACP field updates below): SendMessage rejects running / deleted
// jobs, and applying those updates for a request that is about to be
// rejected leaks the failed send's selection into the next successful run.
// The gate reads a Get() snapshot, so it is best-effort — SendMessage's
// locked check stays authoritative for the residual TOCTOU window.
func (h *Handler) prepareJobSend(ctx context.Context, j *model.Job, req *model.JobMessageRequest) (job.JobRunner, *job.SendMessageOptions, error) {
	if j.Deleted {
		return nil, nil, job.ErrJobDeleted
	}
	if j.Status == model.JobStatusRunning {
		return nil, nil, job.ErrJobRunning
	}

	opts, err := h.prepareJobMessage(j, req)
	if err != nil {
		return nil, nil, err
	}

	if pendingTitle, needSave := h.planJobTitleUpdate(ctx, j, req); needSave {
		j.Title = pendingTitle
		if err := h.jobService.UpdateTitle(j.ID, pendingTitle); err != nil {
			logger.Errorf(ctx, "[job] update title failed: jobId=%s err=%v", j.ID, err)
		}
	}

	return newJobRunner(h, j), opts, nil
}

// planJobTitleUpdate computes a pending title for the job if needed and
// schedules an async LLM-based refinement.
func (h *Handler) planJobTitleUpdate(ctx context.Context, j *model.Job, req *model.JobMessageRequest) (pendingTitle string, needSave bool) {
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
		userMessage := strings.Join(parts, "\n")
		h.asyncUpdateJobTitle(ctx, j.ID, userMessage)
	}
	return pendingTitle, needSave
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

	if opts.SessionID != "" {
		h.maybeUpdateSessionACPFields(opts.SessionID, req.ACPMode, req.ACPThoughtLevel)
	}

	opts.AgentType = req.AgentType
	opts.ModelID = req.ModelID
	opts.ACPMode = req.ACPMode
	opts.ACPThoughtLevel = req.ACPThoughtLevel
	return opts, nil
}

// prepareInteractiveRun handles interactive mode: build messages, resolve session, update model.
//
// All agents (ACP CLIs, including eino-cli) take image inputs as an
// `![image](<abs path>)` text tag prepended to the message content, so the
// image URLs are always preserved verbatim for downstream records (user_input,
// etc.) — nothing is dropped here.
func (h *Handler) prepareInteractiveRun(j *model.Job, req *model.JobMessageRequest) (*job.SendMessageOptions, error) {
	nowMs := time.Now().UnixMilli()
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

	// Stamp user messages with a receive timestamp and a stable msg_id so
	// that history reload can display timestamps and produce stable IDs
	// (avoiding duplicate bubbles when a reload re-indexes messages).
	for i, msg := range msgs {
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

	opts := &job.SendMessageOptions{
		ClientMessageID: req.ClientMessageID,
		Messages:        msgs,
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
		if ok && s.Type != "" && s.Type != req.AgentType {
			if req.SessionID != "" {
				return nil, fmt.Errorf("session %s agentType=%s does not match request agentType=%s", sessionID, s.Type, req.AgentType)
			}
			sessionID = ""
		}
	}

	if sessionID != "" {
		h.maybeUpdateSessionModel(sessionID, req.ModelID)
		opts.SessionID = sessionID
	}
	return opts, nil
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
