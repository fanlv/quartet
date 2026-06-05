package job

import (
	"context"
	"strings"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
	"github.com/google/uuid"
)

// maxShellAccumulatedContent caps the in-memory accumulation of shell stdout
// in `content` to bound the worst-case footprint of a single shell step
// (one user-supplied script can produce GB of output, e.g. a verbose build
// or a recursive find). Aligned with maxStderrSize (10 MB) so stdout and
// stderr share the same bound. SSE deltas are always published live and are
// not affected; only AccumulatedContent() — used for the persisted
// IterationResult.Content and the chat-history assistant message — is
// truncated, with a trailing marker so the cause is obvious on reload.
const maxShellAccumulatedContent = 10 << 20

// loopEventHandler implements agui.EventHandler and forwards agent events
// to the Job's SSE subscribers with loop context (jobID, path).
// It is intentionally not goroutine-safe: a single agent runner callback stream
// must invoke its methods sequentially for one step/turn.
type loopEventHandler struct {
	// ctx is the loop/iteration context captured at construction. It is
	// passed to logger.*f calls and usage accumulator methods so that:
	//   - log entries inherit the loop's source attribution
	//   - any future I/O inside usage.On* (e.g. remote tokenizer) honours
	//     iteration cancellation
	// The handler's lifetime is one step/turn, identical to the ctx the
	// caller threads into RunIteration, so storing it here is safe.
	ctx context.Context

	jobID     string
	sessionID string
	path      []int
	publisher eventPublisher

	tokens    int
	runID     string
	msgID     string
	shellMode bool            // when true, TextMessage events carry isShellOutput external
	content   strings.Builder // accumulates assistant message content

	// contentTruncated is set the first time a shell-mode delta would push
	// `content` past maxShellAccumulatedContent. Once set, further deltas
	// are dropped from `content` (SSE delivery is unaffected) so a runaway
	// shell script can't OOM the process via the accumulator.
	contentTruncated bool

	// currentMessageBuf is reset on every OnMessageStart / OnThoughtStart
	// and consumed (tokenize + record) on the matching End. It exists in
	// addition to `content` so per-message token attribution is precise
	// without disturbing the step-level AccumulatedContent buffer.
	currentMessageBuf strings.Builder

	// nextBoundaryTs, when non-zero, is consumed by the next call to
	// baseEvent() instead of time.Now(). The Builder sets this via
	// SetNextBoundaryTimestamp before invoking a boundary callback so
	// the SSE event carries the same instant that was written to disk.
	nextBoundaryTs int64

	// usage accumulates per-step usage statistics (counts, tokens, per-
	// tool durations). Snapshot()'d at step finalize and handed to the
	// Recorder. Lifetime: same as the loopEventHandler (one per step /
	// turn).
	usage *usagestats.Accumulator
}

type eventPublisher interface {
	Publish(jobID string, event any)
}

var _ agui.EventHandler = (*loopEventHandler)(nil)
var _ agui.BoundaryTimestampSetter = (*loopEventHandler)(nil)

func newLoopEventHandler(ctx context.Context, jobID, sessionID string, path []int, publisher eventPublisher) *loopEventHandler {
	return &loopEventHandler{
		ctx:       ctx,
		jobID:     jobID,
		sessionID: sessionID,
		path:      path,
		publisher: publisher,
		runID:     uuid.New().String(),
		usage:     usagestats.NewAccumulator(),
	}
}

// SetNextBoundaryTimestamp pins the timestamp the next baseEvent() will
// use. Intended for the Builder to share its persist-side clock read
// with the SSE event for the same semantic boundary. Consumed once.
func (h *loopEventHandler) SetNextBoundaryTimestamp(ts int64) {
	h.nextBoundaryTs = ts
}

func (h *loopEventHandler) baseEvent(eventType model.EventType) model.BaseEvent {
	ts := h.nextBoundaryTs
	if ts == 0 {
		ts = nowMillis()
	}
	h.nextBoundaryTs = 0
	be := model.BaseEvent{
		Type:      eventType,
		SessionID: h.sessionID,
		RunID:     h.runID,
		Timestamp: ts,
		JobID:     h.jobID,
		Path:      h.path,
	}
	if h.shellMode {
		be.External = map[string]any{"isShellOutput": true}
	}
	return be
}

func (h *loopEventHandler) OnMessageStart() error {
	h.publishTextStart(false)
	return nil
}

func (h *loopEventHandler) publishTextStart(isThinking bool) {
	h.msgID = uuid.New().String()
	h.currentMessageBuf.Reset()
	event := model.TextMessageStartEvent{
		BaseEvent: h.baseEvent(model.EventTypeTextMessageStart),
		MessageID: h.msgID,
		Role:      model.MessageRoleAssistant,
	}
	h.markThinking(&event.BaseEvent, isThinking)
	h.publisher.Publish(h.jobID, &event)
}

func (h *loopEventHandler) OnMessageDelta(content string) error {
	h.publishTextDelta(content, false, true)
	return nil
}

func (h *loopEventHandler) publishTextDelta(content string, isThinking, appendToContent bool) {
	if appendToContent {
		h.appendAccumulatedContent(content)
	}
	// In shell mode, currentMessageBuf would feed the assistant-text
	// tokenizer at OnMessageEnd. Shell stdout isn't an LLM response, so
	// tokenizing it has no business meaning AND would force a second full
	// copy of the (potentially huge) output through the tokenizer. Skip it.
	if !h.shellMode {
		h.currentMessageBuf.WriteString(content)
	}
	event := model.TextMessageContentEvent{
		BaseEvent: h.baseEvent(model.EventTypeTextMessageContent),
		MessageID: h.msgID,
		Role:      model.MessageRoleAssistant,
		Delta:     content,
	}
	h.markThinking(&event.BaseEvent, isThinking)
	h.publisher.Publish(h.jobID, &event)
}

// appendAccumulatedContent writes to h.content with a 10 MB cap in shell
// mode. Once the cap is hit, a single trailing marker is appended and
// further writes are dropped (SSE delivery still streams the live deltas,
// so the user keeps seeing real-time output). Non-shell paths are
// unbounded — assistant LLM output is naturally bounded by model limits.
func (h *loopEventHandler) appendAccumulatedContent(content string) {
	if !h.shellMode {
		h.content.WriteString(content)
		return
	}
	if h.contentTruncated {
		return
	}
	remaining := maxShellAccumulatedContent - h.content.Len()
	if remaining <= 0 {
		h.contentTruncated = true
		h.content.WriteString("\n... stdout truncated (exceeded 10MB) ...\n")
		return
	}
	if len(content) <= remaining {
		h.content.WriteString(content)
		return
	}
	h.content.WriteString(content[:remaining])
	h.content.WriteString("\n... stdout truncated (exceeded 10MB) ...\n")
	h.contentTruncated = true
}

func (h *loopEventHandler) OnMessageEnd() error {
	if h.usage != nil {
		// Tokenize the assistant text segment that just finished. Uses the
		// per-message buffer (reset at Start) so multiple messages within
		// the same step are attributed independently.
		h.usage.OnAssistantText(h.ctx, h.currentMessageBuf.String())
	}
	h.currentMessageBuf.Reset()
	h.publisher.Publish(h.jobID, &model.TextMessageEndEvent{
		BaseEvent: h.baseEvent(model.EventTypeTextMessageEnd),
		MessageID: h.msgID,
		Role:      model.MessageRoleAssistant,
	})
	return nil
}

func (h *loopEventHandler) LastMessageID() string {
	return h.msgID
}

func (h *loopEventHandler) OnThoughtStart() error {
	h.publishTextStart(true)
	return nil
}

func (h *loopEventHandler) OnThoughtDelta(content string) error {
	h.publishTextDelta(content, true, false)
	return nil
}

func (h *loopEventHandler) OnThoughtEnd() error {
	if h.usage != nil {
		h.usage.OnThoughtText(h.ctx, h.currentMessageBuf.String())
	}
	h.currentMessageBuf.Reset()
	h.publisher.Publish(h.jobID, &model.TextMessageEndEvent{
		BaseEvent: h.baseEvent(model.EventTypeTextMessageEnd),
		MessageID: h.msgID,
		Role:      model.MessageRoleAssistant,
	})
	return nil
}

func (h *loopEventHandler) markThinking(event *model.BaseEvent, isThinking bool) {
	if !isThinking {
		return
	}
	if event.External == nil {
		event.External = make(map[string]any)
	}
	event.External["isThinking"] = true
}

func (h *loopEventHandler) OnToolCallStart(id, name string) error {
	logger.Debugf(h.ctx, "[loopEventHandler.OnToolCallStart] jobId=%s id=%s name=%s", h.jobID, id, name)
	if h.usage != nil {
		h.usage.OnToolCallStart(id, name, nowMillis())
	}
	h.publisher.Publish(h.jobID, &model.ToolCallStartEvent{
		BaseEvent:      h.baseEvent(model.EventTypeToolCallStart),
		ToolCallID:     id,
		ToolCallName:   name,
		ToolCallStatus: model.ToolCallStatusProcessing,
	})
	return nil
}

func (h *loopEventHandler) OnToolCallArgs(id, args string) error {
	if h.usage != nil {
		h.usage.OnToolCallArgsDelta(id, args)
	}
	h.publisher.Publish(h.jobID, &model.ToolCallArgsEvent{
		BaseEvent:      h.baseEvent(model.EventTypeToolCallArgs),
		ToolCallID:     id,
		Delta:          args,
		ToolCallStatus: model.ToolCallStatusProcessing,
	})
	return nil
}

func (h *loopEventHandler) OnToolCallResult(id, content string, success bool) error {
	status := model.ToolCallStatusSuccess
	if !success {
		status = model.ToolCallStatusError
	}
	h.publisher.Publish(h.jobID, &model.ToolCallResultEvent{
		BaseEvent:      h.baseEvent(model.EventTypeToolCallResult),
		ToolCallID:     id,
		Delta:          content,
		ToolCallStatus: status,
	})
	return nil
}

func (h *loopEventHandler) OnToolCallEnd(id string, success bool) error {
	if h.usage != nil {
		h.usage.OnToolCallEnd(h.ctx, id, nowMillis())
	}
	status := model.ToolCallStatusSuccess
	if !success {
		status = model.ToolCallStatusError
	}
	h.publisher.Publish(h.jobID, &model.ToolCallEndEvent{
		BaseEvent:      h.baseEvent(model.EventTypeToolCallEnd),
		ToolCallID:     id,
		ToolCallStatus: status,
	})
	return nil
}

// OnToolCallInterrupted publishes a TOOL_CALL_END with Placeholder status
// so the live UI renders the bubble as "incomplete" (not a green Success
// nor a red Error). Matches how history reload paints synthesised
// placeholder tool results, keeping pre/post-refresh state consistent.
// reason is passed through in External so the UI can surface a tooltip.
func (h *loopEventHandler) OnToolCallInterrupted(id string, reason string) error {
	base := h.baseEvent(model.EventTypeToolCallEnd)
	if reason != "" {
		if base.External == nil {
			base.External = map[string]any{}
		}
		base.External["placeholderReason"] = reason
	}
	h.publisher.Publish(h.jobID, &model.ToolCallEndEvent{
		BaseEvent:      base,
		ToolCallID:     id,
		ToolCallStatus: model.ToolCallStatusPlaceholder,
	})
	return nil
}

// OnToolCallStitched publishes a TOOL_CALL_STITCHED event so the live UI
// rewrites the (already-Finished) Placeholder bubble in place with the
// real terminal payload — matching what history reload would render
// after a refresh. Unlike OnToolCallResult/OnToolCallEnd this targets a
// bubble that the UI has already finalised, so the frontend handler for
// this event explicitly bypasses the "skip if already Finished" guard.
func (h *loopEventHandler) OnToolCallStitched(id string, content string, success bool, supersededAgoMs int64) error {
	if h.usage != nil {
		// The ordinary OnToolCallEnd path bumps tool runtime stats; the
		// stitch path replaces a Placeholder bubble that never made it
		// through OnToolCallEnd, so account it here so dashboards
		// don't undercount tool calls that hit the late-stitch branch.
		h.usage.OnToolCallEnd(h.ctx, id, nowMillis())
	}
	status := model.ToolCallStatusSuccess
	if !success {
		status = model.ToolCallStatusError
	}
	h.publisher.Publish(h.jobID, &model.ToolCallStitchedEvent{
		BaseEvent:       h.baseEvent(model.EventTypeToolCallStitched),
		ToolCallID:      id,
		Delta:           content,
		ToolCallStatus:  status,
		SupersededAgoMs: supersededAgoMs,
	})
	return nil
}

func (h *loopEventHandler) OnTokenUsage(totalTokens int) error {
	h.tokens = totalTokens
	if h.usage != nil {
		h.usage.OnTokenUsage(totalTokens)
	}
	h.publisher.Publish(h.jobID, &model.CustomEvent{
		BaseEvent: h.baseEvent(model.EventTypeCustom),
		Name:      "token_usage",
		Value:     model.TokenUsage{TotalTokens: totalTokens},
	})
	return nil
}

func (h *loopEventHandler) OnError(err error) {
	// User-initiated stop / cancel / timeout is expected control flow, not a system error.
	// The upstream executor handles terminal transitions; suppress duplicate error events here.
	if ctxErr := h.ctx.Err(); ctxErr != nil {
		logger.Debugf(h.ctx, "[loopEventHandler] context done, suppressing error event: jobId=%s sessionId=%s path=%v ctxErr=%v err=%v",
			h.jobID, h.sessionID, h.path, ctxErr, err)
		return
	}
	logger.Errorf(h.ctx, "[loopEventHandler] error: jobId=%s sessionId=%s path=%v err=%v", h.jobID, h.sessionID, h.path, err)
	h.publisher.Publish(h.jobID, &model.RunErrorEvent{
		BaseEvent: h.baseEvent(model.EventTypeRunError),
		Message:   err.Error(),
		Code:      "-1",
	})
}

// AccumulatedContent returns the accumulated assistant message content.
func (h *loopEventHandler) AccumulatedContent() string {
	return h.content.String()
}
