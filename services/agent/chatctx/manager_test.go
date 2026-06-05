package chatctx

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/msgextra"
)

// fakeRepo is an in-memory ChatContextRepo for truncate-tail tests.
type fakeRepo struct {
	// mu guards every field below. Tests stress concurrent BeginRun
	// calls against shared fakeRepo instances; without this every
	// test that spawns goroutines fires the race detector on msgs.
	// WithLock takes the same mu so chatctx.BeginRun's lock-then-mutate
	// flow stays atomic across instances, mirroring the production
	// repository's session-keyed lock.
	mu      sync.Mutex
	msgs    []*schema.Message
	summary *repository.SummaryMessage
	// summaryErr, if set, is returned from LoadSummaryMessage to simulate a
	// read failure on summary.json (distinct from "summary file absent",
	// which is summary==nil + summaryErr==nil).
	summaryErr error
	// replaceErr, if set, is returned from ReplaceMessages to simulate an
	// atomic-rewrite failure (disk full, EACCES on temp file, etc.).
	// BeginRun must NOT silently append the user message when the
	// orphan tail could not be truncated — the next run's successful
	// truncate would cut the freshly-appended user message.
	replaceErr error
	// Track the last snapshot seen by ReplaceMessages so tests can
	// verify atomic rewrite behaviour.
	replacedSnapshots [][]*schema.Message
}

func (r *fakeRepo) LoadAllMessages(context.Context) ([]*schema.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadAllMessagesLocked()
}
func (r *fakeRepo) CountMessage(context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs), nil
}
func (r *fakeRepo) MessagesFingerprint(context.Context) (repository.MessagesFingerprint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Tests don't exercise the ACP drift check, but the production
	// interface requires this method. Returning Count alone (Hash="")
	// keeps the stub minimal while still satisfying the interface.
	return repository.MessagesFingerprint{Count: len(r.msgs)}, nil
}
func (r *fakeRepo) AppendMessages(_ context.Context, msgs []*schema.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appendMessagesLocked(msgs)
}
func (r *fakeRepo) ReplaceMessages(_ context.Context, msgs []*schema.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.replaceMessagesLocked(msgs)
}
func (r *fakeRepo) ReplacePlaceholderToolResult(_ context.Context, toolCallID string, real *schema.Message) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if toolCallID == "" || real == nil {
		return false, nil
	}
	for i, m := range r.msgs {
		if m == nil || m.Role != schema.Tool || m.ToolCallID != toolCallID {
			continue
		}
		if m.Extra == nil {
			continue
		}
		if v, ok := m.Extra[msgextra.KeyPlaceholderToolResult].(bool); !ok || !v {
			continue
		}
		r.msgs[i] = real
		return true, nil
	}
	return false, nil
}
func (r *fakeRepo) LoadSummaryMessage(context.Context) (*repository.SummaryMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadSummaryMessageLocked()
}
func (r *fakeRepo) SaveSummaryMessage(_ context.Context, msg *repository.SummaryMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summary = msg
	return nil
}
func (r *fakeRepo) ClearSummaryMessage(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summary = nil
	return nil
}

// WithLock holds mu for the duration of fn so the LockedRepo handle's
// methods see a consistent view. Production behaviour is identical.
func (r *fakeRepo) WithLock(_ context.Context, fn func(tx repository.LockedRepo) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fn(&fakeLockedRepo{r: r})
}

func (r *fakeRepo) loadAllMessagesLocked() ([]*schema.Message, error) {
	return r.msgs, nil
}
func (r *fakeRepo) loadSummaryMessageLocked() (*repository.SummaryMessage, error) {
	if r.summaryErr != nil {
		return nil, r.summaryErr
	}
	return r.summary, nil
}
func (r *fakeRepo) appendMessagesLocked(msgs []*schema.Message) error {
	r.msgs = append(r.msgs, msgs...)
	return nil
}
func (r *fakeRepo) replaceMessagesLocked(msgs []*schema.Message) error {
	if r.replaceErr != nil {
		return r.replaceErr
	}
	snap := make([]*schema.Message, len(msgs))
	copy(snap, msgs)
	r.replacedSnapshots = append(r.replacedSnapshots, snap)
	r.msgs = snap
	return nil
}

// fakeLockedRepo is the LockedRepo handed to fakeRepo.WithLock callbacks.
// Like production's lockedRepoView, the methods skip mu acquisition
// because the caller already holds it.
type fakeLockedRepo struct{ r *fakeRepo }

func (v *fakeLockedRepo) LoadAllMessages(context.Context) ([]*schema.Message, error) {
	return v.r.loadAllMessagesLocked()
}
func (v *fakeLockedRepo) LoadSummaryMessage(context.Context) (*repository.SummaryMessage, error) {
	return v.r.loadSummaryMessageLocked()
}
func (v *fakeLockedRepo) AppendMessages(_ context.Context, msgs []*schema.Message) error {
	return v.r.appendMessagesLocked(msgs)
}
func (v *fakeLockedRepo) ReplaceMessages(_ context.Context, msgs []*schema.Message) error {
	return v.r.replaceMessagesLocked(msgs)
}

func shellUser(content string) *schema.Message {
	m := schema.UserMessage(content)
	m.Extra = map[string]any{msgextra.KeyShellOutput: true}
	return m
}

func shellAssistant(stdout string) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: stdout,
		Extra:   map[string]any{msgextra.KeyShellOutput: true},
	}
}

func assistantWithToolCalls(content string, toolIDs ...string) *schema.Message {
	tcs := make([]schema.ToolCall, 0, len(toolIDs))
	for _, id := range toolIDs {
		tcs = append(tcs, schema.ToolCall{ID: id, Function: schema.FunctionCall{Name: "T"}})
	}
	return &schema.Message{Role: schema.Assistant, Content: content, ToolCalls: tcs}
}

func toolResult(id, content string) *schema.Message {
	return &schema.Message{Role: schema.Tool, ToolCallID: id, Content: content}
}

// TestBeginRun_NoOpOnCleanHistory: when every tool_call is paired, the
// tail is not modified and BeginRun simply appends the user message.
func TestBeginRun_NoOpOnCleanHistory(t *testing.T) {
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("hi"),
			assistantWithToolCalls("calling", "tc-A"),
			toolResult("tc-A", "A done"),
		},
	}
	m := &ChatContextManager{repo: repo}
	truncated, err := m.BeginRun(context.Background(), schema.UserMessage("next"))
	if err != nil {
		t.Fatalf("BeginRun error: %v", err)
	}
	if truncated {
		t.Fatalf("clean history must not report truncated=true")
	}
	if len(repo.replacedSnapshots) != 0 {
		t.Fatalf("expected no ReplaceMessages, got %d", len(repo.replacedSnapshots))
	}
	if len(repo.msgs) != 4 || repo.msgs[3].Role != schema.User {
		t.Fatalf("expected user message appended, got msgs=%d", len(repo.msgs))
	}
}

// TestBeginRun_TruncatesOrphanAtTail: an assistant with an unmatched
// tool_call at the tail is removed along with everything after it.
func TestBeginRun_TruncatesOrphanAtTail(t *testing.T) {
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("hi"),
			assistantWithToolCalls("r1", "tc-A"),
			toolResult("tc-A", "A done"),
			// Orphan: declared B but no tool result ever arrived.
			assistantWithToolCalls("r2 orphan", "tc-B"),
		},
	}
	m := &ChatContextManager{repo: repo}
	truncated, err := m.BeginRun(context.Background(), schema.UserMessage("next"))
	if err != nil {
		t.Fatalf("BeginRun error: %v", err)
	}
	if !truncated {
		t.Fatalf("orphan tail must report truncated=true so ACP callers can invalidate their subprocess session")
	}
	if len(repo.replacedSnapshots) != 1 {
		t.Fatalf("expected exactly 1 ReplaceMessages (atomic truncate), got %d", len(repo.replacedSnapshots))
	}
	// Snapshot length = 3 (msgs 0..2 kept, orphan assistant dropped).
	if snap := repo.replacedSnapshots[0]; len(snap) != 3 {
		t.Fatalf("truncated snapshot length: got %d want 3", len(snap))
	}
	// After append, final state = truncated (3) + user (1) = 4.
	if len(repo.msgs) != 4 {
		t.Fatalf("final msg count: got %d want 4", len(repo.msgs))
	}
	if repo.msgs[3].Role != schema.User || repo.msgs[3].Content != "next" {
		t.Fatalf("expected final message to be the new user message")
	}
}

// TestBeginRun_PreservesPlaceholderAsPaired: a placeholder role=tool
// entry counts as a valid tool_result and must NOT be treated as an
// orphan.
func TestBeginRun_PreservesPlaceholderAsPaired(t *testing.T) {
	placeholder := toolResult("tc-A", "[placeholder] canceled")
	placeholder.Extra = map[string]any{msgextra.KeyPlaceholderToolResult: true}

	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("hi"),
			assistantWithToolCalls("r1", "tc-A"),
			placeholder,
		},
	}
	m := &ChatContextManager{repo: repo}
	if _, err := m.BeginRun(context.Background(), schema.UserMessage("next")); err != nil {
		t.Fatalf("BeginRun error: %v", err)
	}
	if len(repo.replacedSnapshots) != 0 {
		t.Fatalf("placeholder must count as paired; no truncate expected, got %d", len(repo.replacedSnapshots))
	}
	if len(repo.msgs) != 4 {
		t.Fatalf("expected append to proceed on placeholder-paired tail, got %d", len(repo.msgs))
	}
}

// TestBeginRun_SummaryBoundsTailScan: summary.index defines the scan
// lower bound — messages before summary.index are not scanned (they
// are "under" the summary) so the assistant at index 0 (before summary
// index 2) is NOT considered orphan even if unpaired.
func TestBeginRun_SummaryBoundsTailScan(t *testing.T) {
	// jsonl: [assistant(tc-Y orphan), user, user, assistant(tc-X), tool(tc-X)]
	// summary.index = 3: scan only starts at msgs[3] onwards.
	repo := &fakeRepo{
		msgs: []*schema.Message{
			assistantWithToolCalls("pre-summary orphan", "tc-Y"),
			schema.UserMessage("u1"),
			schema.UserMessage("u2"),
			assistantWithToolCalls("post-summary", "tc-X"),
			toolResult("tc-X", "X done"),
		},
		summary: &repository.SummaryMessage{Index: 3, Message: schema.AssistantMessage("summarised", nil)},
	}
	m := &ChatContextManager{repo: repo}
	if _, err := m.BeginRun(context.Background(), schema.UserMessage("next")); err != nil {
		t.Fatalf("BeginRun error: %v", err)
	}
	if len(repo.replacedSnapshots) != 0 {
		t.Fatalf("scan is bounded by summary.index, pre-summary orphans must be ignored; got %d truncates", len(repo.replacedSnapshots))
	}
}

// TestBeginRun_ShellOutputNotTruncated: shell-executor output sits at the
// tail as role=assistant WITHOUT ToolCalls — the orphan scan must short
// circuit on ToolCalls==nil (manager.go:165) and leave the shell row
// untouched. Regression guard for "tail rewrite eating shell history".
func TestBeginRun_ShellOutputNotTruncated(t *testing.T) {
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("hi"),
			assistantWithToolCalls("r1", "tc-A"),
			toolResult("tc-A", "A done"),
			// Shell step result: a user(script) + assistant(stdout) pair.
			// The assistant row has Extra[shellOutput]=true and NO
			// ToolCalls — it must not look like an orphan.
			shellUser("ls -la"),
			shellAssistant("total 0\ndrwxr-xr-x ..."),
		},
	}
	m := &ChatContextManager{repo: repo}
	if _, err := m.BeginRun(context.Background(), schema.UserMessage("next")); err != nil {
		t.Fatalf("BeginRun error: %v", err)
	}
	if len(repo.replacedSnapshots) != 0 {
		t.Fatalf("shell-output tail must not trigger truncate, got %d", len(repo.replacedSnapshots))
	}
	if len(repo.msgs) != 6 || repo.msgs[5].Role != schema.User || repo.msgs[5].Content != "next" {
		t.Fatalf("expected shell rows preserved + new user appended, got %d msgs last=%+v", len(repo.msgs), repo.msgs[len(repo.msgs)-1])
	}
}

// TestLoadMessagesForLLM_SummaryReadErrorDegradesToAllMessages pins down
// the "summary.json read failure must not block Run" invariant. When the
// summary file is corrupt (not merely absent), LoadMessagesForLLM must
// log-and-degrade to returning the raw message history, matching the
// behaviour of truncateOrphanedTail and the web history handler. The
// alternative — returning error and blocking the whole Run — makes a
// cosmetic compression artefact into a hard availability failure.
func TestLoadMessagesForLLM_SummaryReadErrorDegradesToAllMessages(t *testing.T) {
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("u1"),
			schema.AssistantMessage("a1", nil),
		},
		summaryErr: errors.New("simulated corrupt summary.json"),
	}
	m := &ChatContextManager{repo: repo}

	msgs, err := m.LoadMessagesForLLM(context.Background())
	if err != nil {
		t.Fatalf("LoadMessagesForLLM must not bubble summary read failure: got err=%v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected degradation to full history (2 msgs), got %d", len(msgs))
	}
	if msgs[0].Role != schema.User || msgs[1].Role != schema.Assistant {
		t.Fatalf("degraded history order lost, got %+v", msgs)
	}
}

// TestBeginRun_SummaryLoadErrorFallsBackToFullScan: when the repo returns
// an error from LoadSummaryMessage (summary.json read failure), the scan
// must degrade to a full-file scan (startIdx=0) rather than skipping the
// tail-scrub. This pins down the fallback branch at manager.go:131-136 that
// the summary==nil path does not exercise.
func TestBeginRun_SummaryLoadErrorFallsBackToFullScan(t *testing.T) {
	// Orphan at msgs[0] — under any "bounded by summary.index" scan it
	// would be ignored, but with summary-load failure we fall back to
	// full scan and the orphan gets truncated.
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("hi"),
			assistantWithToolCalls("orphan", "tc-A"), // tc-A never gets a tool result
		},
		summaryErr: errors.New("simulated summary read failure"),
	}
	m := &ChatContextManager{repo: repo}
	if _, err := m.BeginRun(context.Background(), schema.UserMessage("next")); err != nil {
		t.Fatalf("BeginRun error: %v", err)
	}
	if len(repo.replacedSnapshots) != 1 {
		t.Fatalf("summary read failure must still drive full-scan truncate, got %d", len(repo.replacedSnapshots))
	}
	snap := repo.replacedSnapshots[0]
	if len(snap) != 1 || snap[0].Role != schema.User {
		t.Fatalf("expected orphan+tail truncated to just [user(hi)], got %+v", snap)
	}
	if len(repo.msgs) != 2 || repo.msgs[1].Content != "next" {
		t.Fatalf("expected final = [user(hi), user(next)], got %+v", repo.msgs)
	}
}

// TestBeginRun_TruncateFailureDoesNotAppendUser pins down the fix for
// the data-loss hazard where BeginRun used to log-and-continue on
// truncate failure. If the orphan tail can't be cleaned up, appending
// the new user message poisons disk: the next run's truncate starts
// from the same orphan position and cuts the freshly-appended user
// message along with it. BeginRun must surface the error and refuse
// to append so the user can retry cleanly.
func TestBeginRun_TruncateFailureDoesNotAppendUser(t *testing.T) {
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("hi"),
			// Orphan: declared A but no tool result.
			assistantWithToolCalls("r1 orphan", "tc-A"),
		},
		replaceErr: errors.New("simulated disk full on atomic rewrite"),
	}
	m := &ChatContextManager{repo: repo}

	_, err := m.BeginRun(context.Background(), schema.UserMessage("next"))
	if err == nil {
		t.Fatalf("BeginRun must return error when truncate fails, got nil")
	}

	// Original 2 messages preserved; the new user message must NOT have
	// been appended on top of the still-orphaned tail.
	if len(repo.msgs) != 2 {
		t.Fatalf("user message must not be appended when truncate fails: got %d msgs want 2 (msgs=%+v)", len(repo.msgs), repo.msgs)
	}
	if repo.msgs[1].Role != schema.Assistant {
		t.Fatalf("tail must still be the orphan assistant, got %+v", repo.msgs[1])
	}
}

// the "theoretically impossible" scenario of a mid-file orphan followed
// by later completed rounds. Normal flows never produce this layout
// (each Run's entry appends after truncation). If it ever appears on
// disk — e.g. due to a bug in a new writer path — the orphan-scan
// must still cut from the earliest orphan (the safe choice; anything
// after an orphan is suspect), and the anomaly must be visible in
// logs so it is not silently swallowed. See manager.go:229-236.
func TestBeginRun_MidOrphanWithCompleteTailTruncatesAll(t *testing.T) {
	// Layout: [user, assistant(tc-A orphan), assistant(tc-B), tool(tc-B)]
	//                 ^ earliest orphan           ^ complete round AFTER orphan
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("hi"),
			assistantWithToolCalls("r1 orphan", "tc-A"), // tc-A unpaired
			assistantWithToolCalls("r2 complete", "tc-B"),
			toolResult("tc-B", "B done"),
		},
	}
	m := &ChatContextManager{repo: repo}
	if _, err := m.BeginRun(context.Background(), schema.UserMessage("next")); err != nil {
		t.Fatalf("BeginRun error: %v", err)
	}
	if len(repo.replacedSnapshots) != 1 {
		t.Fatalf("expected exactly 1 truncate call, got %d", len(repo.replacedSnapshots))
	}
	snap := repo.replacedSnapshots[0]
	// Earliest orphan was at index 1; truncate keeps only index 0.
	if len(snap) != 1 || snap[0].Role != schema.User {
		t.Fatalf("expected truncation from earliest orphan, got %+v", snap)
	}
	// Final state after append: [user(hi), user(next)] — the complete
	// round after the orphan is intentionally dropped (the safer choice
	// when the invariant is violated).
	if len(repo.msgs) != 2 || repo.msgs[1].Content != "next" {
		t.Fatalf("expected final = [user(hi), user(next)], got %+v", repo.msgs)
	}
}

// TestBeginRun_SummaryIndexBeyondLenFallsBackToFullScan: when summary.index
// points past the end of messages.jsonl (anomaly: corrupt summary.json, a
// crash that left half-written JSONL lines so LoadAllMessages returns fewer
// records than when summary was saved, or an out-of-order write), the scan
// must degrade to a full-file scan rather than silently no-op. The old
// clamp-to-len behaviour turned the orphan scan into nothing and let the
// real tail leak into the next Run.
func TestBeginRun_SummaryIndexBeyondLenFallsBackToFullScan(t *testing.T) {
	// Orphan at msgs[0]; summary.index=99 is nonsense relative to len=2.
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("hi"),
			assistantWithToolCalls("orphan", "tc-A"), // tc-A never gets a tool result
		},
		summary: &repository.SummaryMessage{Index: 99, Message: schema.AssistantMessage("summarised", nil)},
	}
	m := &ChatContextManager{repo: repo}
	if _, err := m.BeginRun(context.Background(), schema.UserMessage("next")); err != nil {
		t.Fatalf("BeginRun error: %v", err)
	}
	if len(repo.replacedSnapshots) != 1 {
		t.Fatalf("summary.index>len must fall back to full-file scan + truncate, got %d snapshots", len(repo.replacedSnapshots))
	}
	snap := repo.replacedSnapshots[0]
	if len(snap) != 1 || snap[0].Role != schema.User {
		t.Fatalf("expected orphan truncated to just [user(hi)], got %+v", snap)
	}
	if len(repo.msgs) != 2 || repo.msgs[1].Content != "next" {
		t.Fatalf("expected final = [user(hi), user(next)], got %+v", repo.msgs)
	}
}

// TestLoadMessagesForLLM_NegativeIndexClamped pins down the lower-bound
// guard for summary.Index. A corrupt summary.json with a negative index
// used to crash the whole Run with a slice-bounds panic at manager.go:69.
// The guard now clamps to 0 and returns summary + full tail, matching
// the write-side clamp in truncateOrphanedTail.
func TestLoadMessagesForLLM_NegativeIndexClamped(t *testing.T) {
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("u1"),
			schema.AssistantMessage("a1", nil),
		},
		summary: &repository.SummaryMessage{Index: -5, Message: schema.AssistantMessage("summarised", nil)},
	}
	m := &ChatContextManager{repo: repo}

	msgs, err := m.LoadMessagesForLLM(context.Background())
	if err != nil {
		t.Fatalf("LoadMessagesForLLM must not error on negative index: got %v", err)
	}
	// Expect summary + full tail (the clamp to 0 means no messages are
	// skipped from allMsgs).
	if len(msgs) != 3 {
		t.Fatalf("expected summary + 2 tail msgs, got %d", len(msgs))
	}
	if msgs[0].Content != "summarised" {
		t.Fatalf("expected first msg to be summary, got %+v", msgs[0])
	}
}

// TestLoadMessagesForLLM_IndexBeyondLenClamped pins down the upper-bound
// guard. A summary.index past len(allMsgs) now clamps to len(allMsgs)
// (returning summary with empty tail) and logs at error level. Silently
// running the append with no clamp would have worked fine here, but the
// error-level log keeps the anomaly observable in ops — matching the
// write-side ANOMALY log in truncateOrphanedTail.
func TestLoadMessagesForLLM_IndexBeyondLenClamped(t *testing.T) {
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("u1"),
			schema.AssistantMessage("a1", nil),
		},
		summary: &repository.SummaryMessage{Index: 99, Message: schema.AssistantMessage("summarised", nil)},
	}
	m := &ChatContextManager{repo: repo}

	msgs, err := m.LoadMessagesForLLM(context.Background())
	if err != nil {
		t.Fatalf("LoadMessagesForLLM must not error on out-of-bounds index: got %v", err)
	}
	// Expect only the summary (no tail because the clamp lands at
	// len(allMsgs)).
	if len(msgs) != 1 {
		t.Fatalf("expected summary-only result when index>len, got %d msgs: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "summarised" {
		t.Fatalf("expected summary as sole msg, got %+v", msgs[0])
	}
}

// TestLoadMessagesForLLM_InjectedSummaryMarker pins down the contract
// with the summarization middleware: LoadMessagesForLLM must tag the
// injected summary with msgextra.KeyIsSummary so Finalize can tell
// "originalMessages contains the old summary" from the message itself,
// without a second summary.json read. Without this marker the
// Finalize path would re-read summary.json to infer "first vs
// subsequent summarization", and any transient read failure between
// the two reads would leave summary.index off by one.
func TestLoadMessagesForLLM_InjectedSummaryMarker(t *testing.T) {
	repo := &fakeRepo{
		msgs: []*schema.Message{
			schema.UserMessage("u1"),
			schema.AssistantMessage("a1", nil),
		},
		summary: &repository.SummaryMessage{Index: 0, Message: schema.AssistantMessage("summarised", nil)},
	}
	m := &ChatContextManager{repo: repo}

	msgs, err := m.LoadMessagesForLLM(context.Background())
	if err != nil {
		t.Fatalf("LoadMessagesForLLM error: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatalf("expected non-empty result with summary injected")
	}
	first := msgs[0]
	if first.Extra == nil {
		t.Fatalf("summary message must carry Extra with the summary marker, got nil")
	}
	v, ok := first.Extra[msgextra.KeyIsSummary].(bool)
	if !ok || !v {
		t.Fatalf("summary message must be marked with %s=true, got Extra=%+v", msgextra.KeyIsSummary, first.Extra)
	}
	// Tail messages must NOT carry the marker.
	for i, tail := range msgs[1:] {
		if tail.Extra == nil {
			continue
		}
		if v, ok := tail.Extra[msgextra.KeyIsSummary].(bool); ok && v {
			t.Fatalf("tail msg[%d] unexpectedly marked as summary: %+v", i, tail)
		}
	}
}

// TestBeginRun_ConcurrentSameSessionSerialises verifies that two
// managers bound to the same sessionID and the same underlying repo
// serialise their truncate+append halves. Without the per-session lock
// the concurrent goroutines would race on fakeRepo.msgs (unsynchronised
// slice), which `go test -race` will flag. The assertion on final
// length is a belt-and-braces correctness check: every appended user
// message must survive, regardless of goroutine order.
func TestBeginRun_ConcurrentSameSessionSerialises(t *testing.T) {
	const sessionID = "sess-race"
	const goroutines = 16
	const msgsPerG = 8

	repo := &fakeRepo{}
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(gID int) {
			defer wg.Done()
			// Each goroutine constructs its own manager to mimic the
			// eino/acp cross-path layout where both paths build fresh
			// ChatContextManager instances against the same sessionID.
			m := &ChatContextManager{repo: repo, sessionID: sessionID}
			for range msgsPerG {
				if _, err := m.BeginRun(context.Background(), schema.UserMessage("u")); err != nil {
					t.Errorf("goroutine %d BeginRun: %v", gID, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	if got, want := len(repo.msgs), goroutines*msgsPerG; got != want {
		t.Fatalf("expected %d appended messages, got %d — append lost under contention", want, got)
	}
}
