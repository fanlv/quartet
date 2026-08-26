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

	tokens        TokenTotals
	providerUsage bool

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

// OnTokenUsage records the last local whole-context estimate as the fallback
// for this turn. Provider usage, when present, remains authoritative.
func (a *Accumulator) OnTokenUsage(total int) {
	a.SetEstimatedUsage(total)
}

// SetProviderUsage stores authoritative provider usage for this turn. Last
// value wins, allowing streaming providers to send cumulative updates. A
// provider report supersedes fallback estimated whole-turn usage, while the
// assistant/thought/toolCall segment estimates remain available.
func (a *Accumulator) SetProviderUsage(usage ProviderTokenUsage) {
	usage.Input = max(0, usage.Input)
	usage.Output = max(0, usage.Output)
	usage.CachedRead = max(0, usage.CachedRead)
	usage.CachedWrite = max(0, usage.CachedWrite)
	usage.Reasoning = max(0, usage.Reasoning)
	usage.Total = max(0, usage.Total)
	if usage.Total <= 0 {
		// A few third-party ACP adapters have emitted a non-nil usage object
		// without filling totalTokens. Preserve the useful provider fields and
		// derive the conventional prompt+completion total instead of replacing a
		// non-zero fallback with zero. Cached read/write are breakdowns of input,
		// not additional tokens. Reasoning is normally included in output; use it
		// only when it is the larger completion-side value.
		usage.Total = saturatingTokenSum(usage.Input, max(usage.Output, usage.Reasoning))
	}
	a.providerUsage = true
	a.tokens.Total = usage.Total
	a.tokens.Reported = usage.Total
	a.tokens.Input = usage.Input
	a.tokens.Output = usage.Output
	a.tokens.CachedRead = usage.CachedRead
	a.tokens.CachedWrite = usage.CachedWrite
	a.tokens.Reasoning = usage.Reasoning
	a.tokens.ReportedTurns = 1
	a.tokens.Estimated = 0
	a.tokens.EstimatedTurns = 0
}

// SetEstimatedUsage stores the local whole-turn fallback. It is ignored once
// provider usage has been observed, and repeated cumulative updates use the
// latest value rather than summing within the turn. The turn is marked as
// estimated even when the amount is zero, so every recorded turn carries
// exactly one source classification.
func (a *Accumulator) SetEstimatedUsage(total int) {
	if a.providerUsage {
		return
	}
	total = max(0, total)
	a.tokens.Estimated = total
	a.tokens.Total = total
	a.tokens.EstimatedTurns = 1
}

// FinalizeEstimate installs the local whole-turn fallback for a turn that never
// received provider usage. inputEstimate is the caller's tokenizer count over
// the turn's input messages; the accumulator contributes its own output-segment
// estimates. Provider-reported turns are left untouched.
func (a *Accumulator) FinalizeEstimate(inputEstimate int) {
	if a == nil || a.providerUsage {
		return
	}
	a.SetEstimatedUsage(inputEstimate + a.tokens.Assistant + a.tokens.Thought + a.tokens.ToolCall)
}

// SetImageEstimate stores the locally estimated image-token subset. It does
// not change Total because those tokens are already included in the provider
// input or local whole-context estimate.
func (a *Accumulator) SetImageEstimate(tokens int) {
	a.tokens.ImageEstimate = max(0, tokens)
}

// HasProviderUsage reports whether this accumulator has received authoritative
// provider usage. It is useful to callers deciding whether a fallback estimate
// still needs to be finalized.
func (a *Accumulator) HasProviderUsage() bool {
	return a != nil && a.providerUsage
}

// Merge adds a completed attempt into this accumulator. Provider and estimated
// coverage are both retained across attempts; this differs intentionally from
// SetProviderUsage, whose replacement semantics apply within one attempt. Open
// tool calls are not merged because they have not contributed usage yet.
func (a *Accumulator) Merge(other *Accumulator) {
	if a == nil || other == nil || a == other {
		return
	}
	a.assistantCount += other.assistantCount
	a.thoughtCount += other.thoughtCount
	a.toolCallCount += other.toolCallCount
	addTokenTotals(&a.tokens, &other.tokens)
	a.providerUsage = a.providerUsage || other.providerUsage
	if len(other.tools) == 0 {
		return
	}
	if a.tools == nil {
		a.tools = make(map[string]*ToolBucket, len(other.tools))
	}
	for key, bucket := range other.tools {
		if bucket == nil {
			continue
		}
		current := a.tools[key]
		if current == nil {
			cp := *bucket
			a.tools[key] = &cp
			continue
		}
		current.Count += bucket.Count
		current.TotalMs += bucket.TotalMs
	}
}

// NormalizeTurnCoverage collapses attempt-level source counters into one
// logical turn. If any attempt required an estimate, the logical turn is
// classified as estimated; otherwise the provider report classifies it as
// reported. Token amounts remain untouched, including provider totals from
// other attempts in a mixed-source retry sequence.
func (a *Accumulator) NormalizeTurnCoverage() {
	if a == nil {
		return
	}
	if a.tokens.EstimatedTurns > 0 {
		a.tokens.EstimatedTurns = 1
		a.tokens.ReportedTurns = 0
		return
	}
	if a.tokens.ReportedTurns > 0 {
		a.tokens.ReportedTurns = 1
	}
}

func saturatingTokenSum(left, right int) int {
	left = max(0, left)
	right = max(0, right)
	maxInt := int(^uint(0) >> 1)
	if left > maxInt-right {
		return maxInt
	}
	return left + right
}

// Snapshot freezes the accumulator into a record that can be sent to the
// Recorder. Pending tool calls (still open at step end) are dropped.
//
// eventID must identify the completed execution stably across retries (for
// example, a job run ID or graph run plus instance key); the recorder uses it
// for durable deduplication and drops snapshots without one. workspaceID,
// modelID, finishedAtMs and durationMs are step-level metadata the caller
// fills in.
func (a *Accumulator) SnapshotWithEventID(eventID, workspaceID, modelID string, finishedAtMs, durationMs int64) Snapshot {
	tools := make(map[string]ToolBucket, len(a.tools))
	for k, v := range a.tools {
		tools[k] = *v
	}
	return Snapshot{
		EventID:        eventID,
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
