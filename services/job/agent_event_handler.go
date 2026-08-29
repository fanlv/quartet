package job

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/tokenizer"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
	"github.com/google/uuid"
)

// agentEventHandler implements agui.EventHandler and forwards agent events
// to the Job's SSE subscribers (stamped with jobID/sessionID/runID).
// It is intentionally not goroutine-safe: a single agent runner callback stream
// must invoke its methods sequentially for one turn.
type agentEventHandler struct {
	// ctx is the run context captured at construction. It is
	// passed to logger.*f calls and usage accumulator methods so that:
	//   - log entries inherit the run's source attribution
	//   - any future I/O inside usage.On* (e.g. remote tokenizer) honours
	//     run cancellation
	// The handler's lifetime is one turn, identical to the ctx the
	// caller threads into RunIteration, so storing it here is safe.
	ctx context.Context

	jobID     string
	sessionID string
	publisher eventPublisher

	tokens        int
	runID         string
	usageEventID  string
	inputEstimate int
	sawTokenUsage bool
	msgID         string
	content       strings.Builder // accumulates assistant message content

	// currentMessageBuf is reset on every OnMessageStart / OnThoughtStart
	// and consumed (tokenize + record) on the matching End. It exists in
	// addition to `content` so per-message token attribution is precise
	// without disturbing the turn-level AccumulatedContent buffer.
	currentMessageBuf strings.Builder

	// nextBoundaryTs, when non-zero, is consumed by the next call to
	// baseEvent() instead of time.Now(). The Builder sets this via
	// SetNextBoundaryTimestamp before invoking a boundary callback so
	// the SSE event carries the same instant that was written to disk.
	nextBoundaryTs int64

	// usage accumulates per-turn usage statistics (counts, tokens, per-
	// tool durations). Snapshot()'d at turn finalize and handed to the
	// Recorder. Lifetime: same as the agentEventHandler (one per turn).
	usage *usagestats.Accumulator
}

type eventPublisher interface {
	Publish(jobID string, event any)
	nowMillis() int64
}

var _ agui.EventHandler = (*agentEventHandler)(nil)
var _ agui.BoundaryTimestampSetter = (*agentEventHandler)(nil)

func newAgentEventHandler(ctx context.Context, jobID, sessionID, clientMessageID string, messages []*schema.Message, publisher eventPublisher) *agentEventHandler {
	runID := uuid.New().String()
	eventID := "job:" + jobID + ":run:" + runID
	if clientMessageID != "" {
		eventID = "job:" + jobID + ":message:" + clientMessageID
	}
	usage := usagestats.NewAccumulator()
	usage.SetImageEstimate(tokenizer.MessagesImageTokenCounter(ctx, messages))
	return &agentEventHandler{
		ctx:           ctx,
		jobID:         jobID,
		sessionID:     sessionID,
		publisher:     publisher,
		runID:         runID,
		usageEventID:  eventID,
		inputEstimate: tokenizer.MessagesTokenCounter(ctx, messages),
		usage:         usage,
	}
}

func (h *agentEventHandler) finalizeUsageEstimate() {
	if h.usage == nil || h.sawTokenUsage {
		return
	}
	h.usage.FinalizeEstimate(h.inputEstimate)
}

// SetNextBoundaryTimestamp pins the timestamp the next baseEvent() will
// use. Intended for the Builder to share its persist-side clock read
// with the SSE event for the same semantic boundary. Consumed once.
func (h *agentEventHandler) SetNextBoundaryTimestamp(ts int64) {
	h.nextBoundaryTs = ts
}

func (h *agentEventHandler) baseEvent(eventType model.EventType) model.BaseEvent {
	ts := h.nextBoundaryTs
	if ts == 0 {
		ts = h.publisher.nowMillis()
	}
	h.nextBoundaryTs = 0
	be := model.BaseEvent{
		Type:      eventType,
		SessionID: h.sessionID,
		RunID:     h.runID,
		Timestamp: ts,
		JobID:     h.jobID,
	}
	return be
}

func (h *agentEventHandler) OnMessageStart() error {
	h.publishTextStart(false)
	return nil
}

func (h *agentEventHandler) publishTextStart(isThinking bool) {
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

func (h *agentEventHandler) OnMessageDelta(content string) error {
	h.publishTextDelta(content, false, true)
	return nil
}

func (h *agentEventHandler) publishTextDelta(content string, isThinking, appendToContent bool) {
	if appendToContent {
		h.content.WriteString(content)
	}
	h.currentMessageBuf.WriteString(content)
	event := model.TextMessageContentEvent{
		BaseEvent: h.baseEvent(model.EventTypeTextMessageContent),
		MessageID: h.msgID,
		Role:      model.MessageRoleAssistant,
		Delta:     content,
	}
	h.markThinking(&event.BaseEvent, isThinking)
	h.publisher.Publish(h.jobID, &event)
}

func (h *agentEventHandler) OnMessageEnd() error {
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

func (h *agentEventHandler) LastMessageID() string {
	return h.msgID
}

func (h *agentEventHandler) OnThoughtStart() error {
	h.publishTextStart(true)
	return nil
}

func (h *agentEventHandler) OnThoughtDelta(content string) error {
	h.publishTextDelta(content, true, false)
	return nil
}

func (h *agentEventHandler) OnThoughtEnd() error {
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

func (h *agentEventHandler) markThinking(event *model.BaseEvent, isThinking bool) {
	if !isThinking {
		return
	}
	if event.External == nil {
		event.External = make(map[string]any)
	}
	event.External["isThinking"] = true
}

func (h *agentEventHandler) OnToolCallStart(id, name string) error {
	logger.Debugf(h.ctx, "[agentEventHandler.OnToolCallStart] jobId=%s id=%s name=%s", h.jobID, id, name)
	if h.usage != nil {
		h.usage.OnToolCallStart(id, name, h.publisher.nowMillis())
	}
	h.publisher.Publish(h.jobID, &model.ToolCallStartEvent{
		BaseEvent:      h.baseEvent(model.EventTypeToolCallStart),
		ToolCallID:     id,
		ToolCallName:   name,
		ToolCallStatus: model.ToolCallStatusProcessing,
	})
	return nil
}

func (h *agentEventHandler) OnToolCallArgs(id, args string, replace bool) error {
	if h.usage != nil {
		if replace {
			h.usage.OnToolCallArgsSnapshot(id, args)
		} else {
			h.usage.OnToolCallArgsDelta(id, args)
		}
	}
	h.publisher.Publish(h.jobID, &model.ToolCallArgsEvent{
		BaseEvent:      h.baseEvent(model.EventTypeToolCallArgs),
		ToolCallID:     id,
		Delta:          args,
		Replace:        replace,
		ToolCallStatus: model.ToolCallStatusProcessing,
	})
	return nil
}

func (h *agentEventHandler) OnToolCallResult(id, content string, success bool) error {
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

func (h *agentEventHandler) OnToolCallEnd(id string, success bool) error {
	if h.usage != nil {
		h.usage.OnToolCallEnd(h.ctx, id, h.publisher.nowMillis())
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
func (h *agentEventHandler) OnToolCallInterrupted(id string, reason string) error {
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
func (h *agentEventHandler) OnToolCallStitched(id string, content string, success bool, supersededAgoMs int64) error {
	if h.usage != nil {
		// The ordinary OnToolCallEnd path bumps tool runtime stats; the
		// stitch path replaces a Placeholder bubble that never made it
		// through OnToolCallEnd, so account it here so dashboards
		// don't undercount tool calls that hit the late-stitch branch.
		h.usage.OnToolCallEnd(h.ctx, id, h.publisher.nowMillis())
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

func (h *agentEventHandler) OnTokenUsage(totalTokens int, estimated bool) error {
	h.tokens = totalTokens
	h.sawTokenUsage = true
	if h.usage != nil {
		h.usage.SetEstimatedUsage(totalTokens)
	}
	h.publisher.Publish(h.jobID, &model.CustomEvent{
		BaseEvent: h.baseEvent(model.EventTypeCustom),
		Name:      "token_usage",
		Value:     model.TokenUsage{TotalTokens: totalTokens, Estimated: estimated},
	})
	return nil
}

func (h *agentEventHandler) OnPromptUsage(usage agui.PromptUsage) error {
	if h.usage != nil {
		h.usage.SetProviderUsage(usagestats.ProviderTokenUsage{
			Total:       nonNegativeUsageInt(usage.TotalTokens),
			Input:       nonNegativeUsageInt(usage.InputTokens),
			Output:      nonNegativeUsageInt(usage.OutputTokens),
			CachedRead:  optionalUsageInt(usage.CachedReadTokens),
			CachedWrite: optionalUsageInt(usage.CachedWriteTokens),
			Reasoning:   optionalUsageInt(usage.ThoughtTokens),
		})
	}
	return nil
}

func optionalUsageInt(value *int64) int {
	if value == nil {
		return 0
	}
	return nonNegativeUsageInt(*value)
}

func nonNegativeUsageInt(value int64) int {
	if value <= 0 {
		return 0
	}
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	return int(value)
}

func (h *agentEventHandler) OnError(err error) {
	// User-initiated stop / cancel / timeout is expected control flow, not a system error.
	// The upstream executor handles terminal transitions; suppress duplicate error events here.
	if ctxErr := h.ctx.Err(); ctxErr != nil {
		logger.Debugf(h.ctx, "[agentEventHandler] context done, suppressing error event: jobId=%s sessionId=%s ctxErr=%v err=%v",
			h.jobID, h.sessionID, ctxErr, err)
		return
	}
	logger.Errorf(h.ctx, "[agentEventHandler] error: jobId=%s sessionId=%s err=%v", h.jobID, h.sessionID, err)
	h.publisher.Publish(h.jobID, &model.RunErrorEvent{
		BaseEvent: h.baseEvent(model.EventTypeRunError),
		Message:   err.Error(),
		Code:      classifyRunErrorCode(err),
	})
}

// AccumulatedContent returns the accumulated assistant message content.
func (h *agentEventHandler) AccumulatedContent() string {
	return h.content.String()
}
