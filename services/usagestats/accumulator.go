package usagestats

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/tokenizer"
)

// Accumulator is held per-step (per chat round / loop iteration / shell
// step) by the loop event handler. It records the running counts and per-
// tool durations as events arrive, then yields a Snapshot at step finalize.
//
// The Accumulator is single-goroutine; the loop event handler is the only
// caller. It does not persist anything itself — the Snapshot it emits is
// what gets handed to the Recorder.
type Accumulator struct {
	assistantCount int
	thoughtCount   int
	toolCallCount  int

	tokens TokenTotals

	// pendingTools[id] is set on OnToolCallStart and cleared on OnToolCallEnd.
	// The map outlives the tool call only if the step ends mid-call; in that
	// case the entry is dropped (no half-attributed time).
	pendingTools map[string]*pendingTool

	// tools collects the per-command bucket. Key is the parsed tool command
	// name (see toolname.Resolve). Created lazily.
	tools map[string]*ToolBucket
}

type pendingTool struct {
	name      string
	startedAt int64
	argsBuf   strings.Builder
}

// NewAccumulator returns a fresh accumulator. The caller should create one
// per step and discard it after Snapshot.
func NewAccumulator() *Accumulator {
	return &Accumulator{}
}

// OnAssistantMessageEnd marks one finished assistant text segment. The
// message argument is the in-memory schema.Message used to estimate
// tokens via the existing tokenizer cache; pass nil to count only.
func (a *Accumulator) OnAssistantMessageEnd(ctx context.Context, msg *schema.Message) {
	a.assistantCount++
	if msg != nil {
		a.tokens.Assistant += tokenizer.MessageTokenCounter(ctx, msg)
	}
}

// OnThoughtMessageEnd marks one finished thought / Deep Think segment.
func (a *Accumulator) OnThoughtMessageEnd(ctx context.Context, msg *schema.Message) {
	a.thoughtCount++
	if msg != nil {
		a.tokens.Thought += tokenizer.MessageTokenCounter(ctx, msg)
	}
}

// OnAssistantText / OnThoughtText are the no-message fallbacks used when
// the loop_event_handler does not have a schema.Message to pass (e.g.
// during streaming the text is concatenated locally). The cost is one
// tokenize pass over the raw text.
func (a *Accumulator) OnAssistantText(ctx context.Context, text string) {
	a.assistantCount++
	a.tokens.Assistant += estimateText(ctx, schema.Assistant, text)
}

func (a *Accumulator) OnThoughtText(ctx context.Context, text string) {
	a.thoughtCount++
	a.tokens.Thought += estimateText(ctx, schema.Assistant, text)
}

// OnToolCallStart records the starting timestamp and tool name for one
// in-flight tool call. id is the runtime tool-call id (unique within the
// step). startedAtMs is the wall-clock unix-millis at the moment the call
// began (caller-provided so the same clock read can be reused elsewhere).
func (a *Accumulator) OnToolCallStart(id, name string, startedAtMs int64) {
	if id == "" {
		return
	}
	if a.pendingTools == nil {
		a.pendingTools = make(map[string]*pendingTool)
	}
	a.pendingTools[id] = &pendingTool{name: name, startedAt: startedAtMs}
}

// OnToolCallArgsDelta accumulates streaming args deltas onto the matching
// pending tool. Buffer is later parsed by Resolve to extract the command
// name for shell-class tools.
func (a *Accumulator) OnToolCallArgsDelta(id, delta string) {
	if id == "" || delta == "" || a.pendingTools == nil {
		return
	}
	pt, ok := a.pendingTools[id]
	if !ok {
		return
	}
	pt.argsBuf.WriteString(delta)
}

// OnToolCallArgsSnapshot replaces the arguments collected for a pending tool.
// ACP reports rawInput as a complete value, so appending repeated snapshots
// would inflate token usage and prevent command-name resolution.
func (a *Accumulator) OnToolCallArgsSnapshot(id, args string) {
	if id == "" || a.pendingTools == nil {
		return
	}
	pt, ok := a.pendingTools[id]
	if !ok {
		return
	}
	pt.argsBuf.Reset()
	pt.argsBuf.WriteString(args)
}

// OnToolCallEnd marks one tool call as completed: increments the global
// toolcall count, attributes the duration to the parsed command name, and
// adds the (name + args) tokenize estimate to the toolCall token bucket.
//
// finishedAtMs is the wall-clock unix-millis at completion. If the call
// has no matching pending entry (interrupted / out-of-order), the call is
// dropped — neither count nor duration is recorded, by design (no
// half-attributed time).
func (a *Accumulator) OnToolCallEnd(ctx context.Context, id string, finishedAtMs int64) {
	if id == "" || a.pendingTools == nil {
		return
	}
	pt, ok := a.pendingTools[id]
	if !ok {
		return
	}
	delete(a.pendingTools, id)

	a.toolCallCount++
	dur := finishedAtMs - pt.startedAt
	if dur < 0 {
		dur = 0
	}

	args := pt.argsBuf.String()
	bucketKey := ResolveToolBucketKey(pt.name, args)
	if a.tools == nil {
		a.tools = make(map[string]*ToolBucket)
	}
	tb, ok := a.tools[bucketKey]
	if !ok {
		tb = &ToolBucket{}
		a.tools[bucketKey] = tb
	}
	tb.Count++
	tb.TotalMs += dur

	a.tokens.ToolCall += estimateText(ctx, schema.Assistant, pt.name+args)
}

// OnTokenUsage stores the whole-round total token estimate emitted by
// OnTokenUsage. Last value wins (callers typically emit once per step).
func (a *Accumulator) OnTokenUsage(total int) {
	a.tokens.Total = total
}

// Snapshot freezes the accumulator into a record that can be sent to the
// Recorder. Pending tool calls (still open at step end) are dropped.
// workspaceID, modelID, finishedAtMs and durationMs are step-level metadata
// the caller fills in.
func (a *Accumulator) Snapshot(workspaceID, modelID string, finishedAtMs, durationMs int64) Snapshot {
	tools := make(map[string]ToolBucket, len(a.tools))
	for k, v := range a.tools {
		tools[k] = *v
	}
	return Snapshot{
		WorkspaceID:    workspaceID,
		ModelID:        modelID,
		FinishedAtMs:   finishedAtMs,
		DurationMs:     durationMs,
		AssistantCount: a.assistantCount,
		ThoughtCount:   a.thoughtCount,
		ToolCallCount:  a.toolCallCount,
		Tokens:         a.tokens,
		Tools:          tools,
	}
}

// estimateText runs the tokenizer over a short ad-hoc string. Used when the
// caller does not have a schema.Message handy.
func estimateText(ctx context.Context, role schema.RoleType, text string) int {
	if text == "" {
		return 0
	}
	msg := &schema.Message{Role: role, Content: text}
	return tokenizer.MessageTokenCounter(ctx, msg)
}

// jsonArgPeekResult tries to recover args.command from a maybe-incomplete
// JSON stream. Returns (commandString, true) on successful parse with a
// string `command` field; (commandString, true) where commandString is ""
// when JSON parsed but the field is missing or non-string; ("", false)
// when the args couldn't be parsed as JSON at all. Exposed as an internal
// helper so toolname.go can distinguish "parsed empty" from "parse failed".
func jsonArgPeekResult(args string) (string, bool) {
	args = strings.TrimSpace(args)
	if args == "" {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return "", false
	}
	if v, ok := m["command"]; ok {
		if s, ok := v.(string); ok {
			return s, true
		}
	}
	return "", true
}
