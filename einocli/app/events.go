package app

import (
	"context"
	"os"
	"strconv"
	"sync"

	acp "github.com/eino-contrib/acp"
	acpconn "github.com/eino-contrib/acp/conn"
	"github.com/fanlv/quartet/einocli/logger"
	"github.com/fanlv/quartet/einocli/types/agui"
	"github.com/fanlv/quartet/einocli/types/msgextra"
)

// defaultContextSize is the usage_update.size fallback; EINO_CLI_CONTEXT_SIZE
// overrides it.
const defaultContextSize int64 = 200000

var contextSize = sync.OnceValue(func() int64 {
	if v := os.Getenv("EINO_CLI_CONTEXT_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return int64(n)
		}
	}
	return defaultContextSize
})

// eventTranslator converts the runtime's agui event stream into ACP
// session/update notifications for one prompt turn (or one history replay).
//
// The runtime invokes handler callbacks sequentially for a run (see
// agui.EventHandler), so the translator needs no locking. Boundary/start/end
// callbacks carry no ACP counterpart and are intentional no-ops; the
// BoundaryTimestampSetter capability is deliberately NOT implemented.
type eventTranslator struct {
	conn *acpconn.AgentConnection
	sid  acp.SessionID
	// ctx is detached from prompt cancellation so the run's terminal cleanup
	// events (interrupted placeholders, final token usage) still reach the
	// client after a cancel.
	ctx context.Context

	// toolArgs accumulates each tool call's argument text so args updates can
	// carry the cumulative snapshot the client expects (replace semantics).
	toolArgs map[string]string
}

var _ agui.EventHandler = (*eventTranslator)(nil)

func newEventTranslator(ctx context.Context, conn *acpconn.AgentConnection, sid string) *eventTranslator {
	return &eventTranslator{
		conn:     conn,
		sid:      acp.SessionID(sid),
		ctx:      ctx,
		toolArgs: map[string]string{},
	}
}

// emit sends one session/update notification. Delivery is best-effort: a
// dead client turns sends into errors, logged at Debug so a teardown race
// never fails the turn.
func (t *eventTranslator) emit(update acp.SessionUpdate) {
	if t.conn == nil {
		return
	}
	if err := t.conn.SessionUpdate(t.ctx, acp.SessionNotification{SessionID: t.sid, Update: update}); err != nil {
		logger.Debugf(context.Background(), "[acp] session update send failed: session=%s err=%v", t.sid, err)
	}
}

func textChunk(text string) acp.ContentChunk {
	return acp.ContentChunk{Content: acp.NewContentBlockText(acp.TextContent{Text: text})}
}

func textToolCallContent(text string) []acp.ToolCallContent {
	return []acp.ToolCallContent{
		acp.NewToolCallContentContent(acp.Content{Content: acp.NewContentBlockText(acp.TextContent{Text: text})}),
	}
}

// emitUserText is used by replay to re-stream a user message.
func (t *eventTranslator) emitUserText(text string) {
	t.emit(acp.NewSessionUpdateUserMessageChunk(textChunk(text)))
}

// emitToolCallDeclared emits the tool_call notification (status in_progress).
func (t *eventTranslator) emitToolCallDeclared(id, title string) {
	status := acp.ToolCallStatusInProgress
	t.emit(acp.NewSessionUpdateToolCall(acp.ToolCall{
		ToolCallID: acp.ToolCallID(id),
		Title:      title,
		Status:     &status,
	}))
}

// emitToolArgs emits an in_progress tool_call_update with the cumulative args.
func (t *eventTranslator) emitToolArgs(id, cumulativeArgs string) {
	status := acp.ToolCallStatusInProgress
	t.emit(acp.NewSessionUpdateToolCallUpdate(acp.ToolCallUpdate{
		ToolCallID: acp.ToolCallID(id),
		Status:     &status,
		Content:    textToolCallContent(cumulativeArgs),
	}))
}

// emitToolTerminal emits the terminal tool_call_update. Every declared tool
// call must receive exactly one of these.
func (t *eventTranslator) emitToolTerminal(id, content string, success bool) {
	status := acp.ToolCallStatusCompleted
	if !success {
		status = acp.ToolCallStatusFailed
	}
	t.emit(acp.NewSessionUpdateToolCallUpdate(acp.ToolCallUpdate{
		ToolCallID: acp.ToolCallID(id),
		Status:     &status,
		Content:    textToolCallContent(content),
	}))
}

// --- agui.MessageHandler ---

func (t *eventTranslator) OnMessageStart() error { return nil }

func (t *eventTranslator) OnMessageDelta(content string) error {
	t.emit(acp.NewSessionUpdateAgentMessageChunk(textChunk(content)))
	return nil
}

func (t *eventTranslator) OnMessageEnd() error { return nil }

func (t *eventTranslator) LastMessageID() string { return "" }

// --- agui.ThoughtHandler ---

func (t *eventTranslator) OnThoughtStart() error { return nil }

func (t *eventTranslator) OnThoughtDelta(content string) error {
	t.emit(acp.NewSessionUpdateAgentThoughtChunk(textChunk(content)))
	return nil
}

func (t *eventTranslator) OnThoughtEnd() error { return nil }

// --- agui.ToolCallHandler ---

func (t *eventTranslator) OnToolCallStart(id, name string) error {
	t.emitToolCallDeclared(id, name)
	return nil
}

func (t *eventTranslator) OnToolCallArgs(id, args string, replace bool) error {
	if replace {
		t.toolArgs[id] = args
	} else {
		t.toolArgs[id] += args
	}
	t.emitToolArgs(id, t.toolArgs[id])
	return nil
}

func (t *eventTranslator) OnToolCallResult(id, content string, success bool) error {
	t.emitToolTerminal(id, content, success)
	return nil
}

func (t *eventTranslator) OnToolCallEnd(_ string, _ bool) error { return nil }

func (t *eventTranslator) OnToolCallInterrupted(id, reason string) error {
	t.emitToolTerminal(id, msgextra.PlaceholderPrefix+" "+reason, false)
	return nil
}

func (t *eventTranslator) OnToolCallStitched(id, content string, success bool, _ int64) error {
	t.emitToolTerminal(id, content, success)
	return nil
}

// --- agui.TokenUsageHandler ---

func (t *eventTranslator) OnTokenUsage(totalTokens int) error {
	if totalTokens <= 0 {
		return nil
	}
	t.emit(acp.NewSessionUpdateUsageUpdate(acp.UsageUpdate{
		Used: int64(totalTokens),
		Size: contextSize(),
	}))
	return nil
}

// --- agui EventHandler error hook ---

func (t *eventTranslator) OnError(err error) {
	logger.Debugf(context.Background(), "[acp] run error event: session=%s err=%v", t.sid, err)
}
