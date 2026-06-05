package chatctx

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/msgextra"
)

// SessionToucher is the narrow interface chatctx uses to bump a session's
// UpdatedAt without directly owning session metadata storage. Satisfied by
// services/session.Service so every UpdatedAt write funnels through the
// single in-memory-and-disk writer.
type SessionToucher interface {
	Touch(sessionID string) error
}

type ChatContextManager struct {
	repo      repository.ChatContextRepo
	toucher   SessionToucher
	sessionID string
}

// New builds a manager bound to one session. toucher + sessionID are used by
// BeginRun to bump session meta's UpdatedAt; passing nil / "" disables the
// touch (used by unit tests that stub only ChatContextRepo).
func New(repo repository.ChatContextRepo, toucher SessionToucher, sessionID string) *ChatContextManager {
	return &ChatContextManager{repo: repo, toucher: toucher, sessionID: sessionID}
}

// CountMessages returns the on-disk message count. Errors are surfaced to
// the caller rather than silenced, because every call site feeds a
// correctness-sensitive ACP drift / sync decision: a swallowed I/O or
// parse failure would be indistinguishable from "empty history" and
// trigger a spurious subprocess reset that drops conversation context.
func (m *ChatContextManager) CountMessages(ctx context.Context) (int, error) {
	count, err := m.repo.CountMessage(ctx)
	if err != nil {
		return 0, fmt.Errorf("count messages failed: %w", err)
	}
	return count, nil
}

// MessagesFingerprint returns the (count, content-hash) pair the ACP
// drift check uses to detect cross-path mutations. Wraps the repo so
// errors carry a consistent prefix; ctx gates both the lock acquisition
// (via the repo's ctx-aware mutex) and pre-IO short-circuit checks.
func (m *ChatContextManager) MessagesFingerprint(ctx context.Context) (repository.MessagesFingerprint, error) {
	fp, err := m.repo.MessagesFingerprint(ctx)
	if err != nil {
		return repository.MessagesFingerprint{}, fmt.Errorf("compute messages fingerprint failed: %w", err)
	}
	return fp, nil
}

func (m *ChatContextManager) LoadMessagesForLLM(ctx context.Context) ([]*schema.Message, error) {
	// summary.json is optional context compression — a read error (corrupt
	// JSON, transient I/O) must not block the whole Run. Degrade to
	// "no summary" and log, matching truncateOrphanedTail and the web
	// history handler. The write side remains the source of truth; a
	// transient read error hiding the rest of the conversation would be a
	// worse failure mode than sending the uncompressed tail to the LLM.
	summary, err := m.repo.LoadSummaryMessage(ctx)
	if err != nil {
		logger.Warnf(ctx, "[ChatContextManager] load summary for LLM failed, degrading to no summary: %v", err)
		summary = nil
	}

	allMsgs, err := m.repo.LoadAllMessages(ctx)
	if err != nil {
		return nil, fmt.Errorf("load all messages failed: %w", err)
	}

	if summary == nil || summary.Message == nil {
		return allMsgs, nil
	}

	// Clamp summary.Index into [0, len(allMsgs)]. The two anomalies we
	// defend against here mirror the invariants enforced on the write
	// side (truncateOrphanedTail, manager.go:195-211):
	//   - idx < 0: impossible under a well-formed summary; guards
	//     against data corruption / hand-edits. Without this clamp the
	//     allMsgs[idx:] slice below would panic.
	//   - idx > len(allMsgs): summary claims to cover messages that
	//     don't exist on disk. Triggers: partial crash between
	//     summary.json write and messages.jsonl flush, unparseable JSONL
	//     lines skipped by LoadAllMessages, or an out-of-order restore.
	//     The LLM will only see the summary (no tail) and the anomaly
	//     is logged at error level so it is observable in ops.
	idx := summary.Index
	if idx < 0 {
		logger.Errorf(ctx, "[ChatContextManager] ANOMALY: summary.index=%d is negative, clamping to 0", idx)
		idx = 0
	}
	if idx > len(allMsgs) {
		logger.Errorf(ctx, "[ChatContextManager] ANOMALY: summary.index=%d exceeds len(msgs)=%d; LLM will see summary-only tail. Investigate summary.json / messages.jsonl consistency.", idx, len(allMsgs))
		idx = len(allMsgs)
	}

	var result []*schema.Message
	// Mark the summary message so downstream middlewares (specifically
	// summarization's Finalize) can reliably tell "originalMessages[i]
	// is the old summary we injected" without depending on a second
	// LoadSummaryMessage read succeeding. summary.Message is always a
	// fresh unmarshal from summary.json (repo guarantees), so mutating
	// Extra here does not pollute any shared cache.
	if summary.Message.Extra == nil {
		summary.Message.Extra = make(map[string]any)
	}
	summary.Message.Extra[msgextra.KeyIsSummary] = true
	result = append(result, summary.Message)
	result = append(result, allMsgs[idx:]...)

	logger.Debugf(ctx, "load messages for llm, summary index=%d, msg count=%d", idx, len(result))
	return result, nil
}

func (m *ChatContextManager) AppendMessages(ctx context.Context, msgs ...*schema.Message) error {
	if err := m.repo.AppendMessages(ctx, msgs); err != nil {
		logger.Errorf(ctx, "[ChatContextManager] persist messages failed (count=%d): %v", len(msgs), err)
		return err
	}
	return nil
}

// ReplacePlaceholderToolResult swaps a previously-persisted placeholder
// tool_result (written by the round builder on an eager superseded flush)
// with the real terminal result that arrived late. Logs at debug on
// success, info when the placeholder is gone (summary compaction), and
// error on I/O failure. Callers typically pair this with an in-memory
// fix-up of the builder's completedMessages so a subsequent
// CollectMessages sees the real result too.
func (m *ChatContextManager) ReplacePlaceholderToolResult(ctx context.Context, toolCallID string, real *schema.Message) (bool, error) {
	stitched, err := m.repo.ReplacePlaceholderToolResult(ctx, toolCallID, real)
	if err != nil {
		logger.Errorf(ctx, "[ChatContextManager] stitch placeholder tool_result failed: toolCallID=%s err=%v", toolCallID, err)
		return false, err
	}
	if !stitched {
		logger.Infof(ctx, "[ChatContextManager] stitch placeholder tool_result: nothing to replace (summary compaction or already-stitched): toolCallID=%s", toolCallID)
		return false, nil
	}
	logger.Debugf(ctx, "[ChatContextManager] stitched placeholder tool_result with real content: toolCallID=%s", toolCallID)
	return true, nil
}

// BeginRun is the shared "user message entry" hook for every agent path
// (eino / acp / future). It performs three disk operations before handing
// control back to the caller:
//
//  1. Scans messages.jsonl from summary.index (or 0 if summary missing)
//     forward and truncates the earliest orphan round — any assistant
//     message whose ToolCalls contain ids without a matching role=tool
//     result (real or placeholder). Truncation is atomic via
//     repo.ReplaceMessages (write-to-temp + rename). No-op when the tail
//     is clean.
//  2. Appends the user messages.
//  3. Touches session meta's UpdatedAt so the UI's "last active" field
//     reflects the new turn. No-op when no SessionRepo was wired.
//
// This cleans up residue from non-graceful exits (SIGKILL / panic /
// flush write failures) before the new Run sees the on-disk history,
// keeping tool_use / tool_result paired in every flush downstream.
//
// Returns truncated=true iff step 1 actually rewrote the file. ACP
// callers use this to discard their in-memory subprocess session —
// once disk history diverges from the subprocess's view of the
// conversation, continuing to prompt the old session would feed the
// model an inconsistent tail. Callers that don't own a subprocess
// session (eino) can ignore the flag.
//
// This is intentionally kept in manager.go (not in each path's Run) so
// the invariant "append user message only after orphan tail is gone"
// cannot drift between paths. Read-side LoadMessagesForLLM does no
// further tail scrubbing.
//
// Truncation failure is fatal and propagates back to the caller WITHOUT
// appending the user message. Appending on top of a still-orphaned tail
// is a data-loss hazard: the next run's truncate would succeed from the
// same orphan position and cut the freshly-appended user message along
// with it. Returning here lets the caller surface the error (the Run
// entry points both wrap it as "persist user messages failed") and the
// user can retry, at which point the next BeginRun will re-attempt the
// truncate from a clean starting point.
//
// Concurrency: truncate+append run inside repo.WithLock so independent
// ChatContextRepo instances pointed at the same session directory
// (eino / acp / shell-persist) cannot interleave a write between the
// two steps and corrupt history. The lock is owned by the repository
// layer (repository/session_locks.go) so every write entry-point on
// the same sessionDir shares it, regardless of which package built
// the repo instance.
func (m *ChatContextManager) BeginRun(ctx context.Context, userMessages ...*schema.Message) (bool, error) {
	var truncated bool
	err := m.repo.WithLock(ctx, func(tx repository.LockedRepo) error {
		t, err := m.truncateOrphanedTailLocked(ctx, tx)
		if err != nil {
			return fmt.Errorf("truncate orphaned tail failed: %w", err)
		}
		truncated = t
		if err := tx.AppendMessages(ctx, userMessages); err != nil {
			logger.Errorf(ctx, "[ChatContextManager] persist messages failed (count=%d): %v", len(userMessages), err)
			return err
		}
		return nil
	})
	if err != nil {
		return truncated, err
	}
	// Touch session meta.UpdatedAt so the UI's "last active" timestamp
	// tracks conversation activity. Non-fatal on failure — the tick is
	// cosmetic, not a correctness invariant. Outside the lock so a slow
	// session-meta writer can't block the next AppendMessages.
	m.touchSessionMeta(ctx)
	return truncated, nil
}

// touchSessionMeta bumps meta.UpdatedAt. No-op when the manager was
// constructed without a SessionToucher (e.g. unit tests).
func (m *ChatContextManager) touchSessionMeta(ctx context.Context) {
	if m.toucher == nil || m.sessionID == "" {
		return
	}
	if err := m.toucher.Touch(m.sessionID); err != nil {
		logger.Warnf(ctx, "[ChatContextManager] touch session meta failed: sessionId=%s err=%v", m.sessionID, err)
	}
}

// truncateOrphanedTailLocked scans the tail of messages.jsonl starting
// from summary.index and removes any trailing segment that begins with
// an assistant message whose ToolCalls are not fully paired with
// role=tool results. If nothing orphaned is found, the file is
// untouched.
//
// Must be called with the per-session write lock already held — the
// caller passes a LockedRepo handle obtained from
// ChatContextRepo.WithLock so the load + replace pair is atomic with
// respect to other ChatContextRepo instances on the same session.
//
// Returns truncated=true iff the file was actually rewritten. Callers
// that own additional in-memory state synced from this file (e.g. an
// ACP subprocess session) can use this to invalidate that state.
//
// Pairing rule: a ToolCall is "paired" iff a role=tool message with the
// same ToolCallID appears somewhere after the declaring assistant
// message in messages.jsonl. Placeholders (KeyPlaceholderToolResult =
// true) count as paired — they are already legal tool_result entries
// for LLM schema validation.
//
// Shell executor rows (role=assistant, ToolCalls empty) never trigger
// truncation because they have no tool_calls.
func (m *ChatContextManager) truncateOrphanedTailLocked(ctx context.Context, tx repository.LockedRepo) (bool, error) {
	msgs, err := tx.LoadAllMessages(ctx)
	if err != nil {
		return false, fmt.Errorf("load messages for orphan scan failed: %w", err)
	}
	if len(msgs) == 0 {
		return false, nil
	}

	startIdx := 0
	summary, err := tx.LoadSummaryMessage(ctx)
	if err != nil {
		// summary.json is optional; if we can't read it, degrade to a
		// full-file scan rather than skipping the tail-scrub.
		logger.Warnf(ctx, "[ChatContextManager] load summary for orphan scan failed, scanning full file: %v", err)
	} else if summary != nil {
		startIdx = summary.Index
	}
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx > len(msgs) {
		// summary.index ahead of the actual on-disk message count is
		// always anomalous — a well-formed summary can only cover
		// messages that exist. Triggers: corrupt summary.json, a write
		// that advanced index before appending the corresponding
		// messages, or an earlier JSONL crash that left unparseable
		// lines so LoadAllMessages returned fewer records than when
		// summary was last saved. Silently clamping to len(msgs) turns
		// the orphan scan into a no-op and lets a real tail leak into
		// the next Run's prompt. Fall back to a full-file scan and log
		// so the anomaly is observable.
		logger.Errorf(ctx, "[ChatContextManager] ANOMALY: summary.index=%d exceeds len(msgs)=%d, falling back to full-file orphan scan", startIdx, len(msgs))
		startIdx = 0
	}

	// Walk the scan window forward, collecting for each assistant the
	// set of tool_call_ids still missing a matching role=tool entry.
	// Record the position of the earliest unresolved assistant. As soon
	// as a later role=tool satisfies one of those ids, drop it from the
	// pending set; if the pending set is still non-empty at EOF, that
	// assistant (and everything after it) is an orphan tail.
	//
	// byID gives O(1) lookup when a tool_result arrives. tool_call_ids
	// are globally unique in well-formed streams, so one id maps to at
	// most one pending; degenerate upstreams that reuse ids keep the
	// earliest-declaring pending (first-writer-wins), matching the
	// "earliest open pending" tiebreaker the linear scan used before.
	// pendings preserves declaration order so the final "pick earliest
	// orphan" scan is deterministic.
	type pending struct {
		pos    int
		missed map[string]struct{}
	}
	var pendings []*pending
	byID := make(map[string]*pending)

	for i := startIdx; i < len(msgs); i++ {
		m := msgs[i]
		if m == nil {
			continue
		}
		switch m.Role {
		case schema.Assistant:
			if len(m.ToolCalls) == 0 {
				continue
			}
			p := &pending{pos: i, missed: make(map[string]struct{}, len(m.ToolCalls))}
			for _, tc := range m.ToolCalls {
				if tc.ID == "" {
					continue
				}
				p.missed[tc.ID] = struct{}{}
				if _, exists := byID[tc.ID]; !exists {
					byID[tc.ID] = p
				}
			}
			if len(p.missed) == 0 {
				continue
			}
			pendings = append(pendings, p)
		case schema.Tool:
			if m.ToolCallID == "" {
				continue
			}
			// O(1) lookup replaces the former linear walk over
			// pendings. Once satisfied we drop the id from both
			// the pending's missed set and the byID index so a
			// stray second tool_result for the same id cannot
			// "re-satisfy" an already-cleared pending.
			p, ok := byID[m.ToolCallID]
			if !ok {
				continue
			}
			delete(p.missed, m.ToolCallID)
			delete(byID, m.ToolCallID)
		}
	}

	// Pick the earliest assistant that still has unresolved ids. If
	// everything is paired, the file is clean.
	//
	// Guard: if there are later pendings whose tool_calls were fully
	// paired (i.e. complete rounds sit AFTER the earliest orphan), this
	// is a "mid-orphan + completed tail" layout that normal flows never
	// produce — truncation still happens from the earliest orphan (the
	// correct conservative choice), but we log an error so the anomaly
	// is observable instead of silently dropping valid history.
	cutPos := -1
	orphanPendingIdx := -1
	for i, p := range pendings {
		if len(p.missed) > 0 {
			cutPos = p.pos
			orphanPendingIdx = i
			break
		}
	}
	if cutPos < 0 {
		return false, nil
	}
	var completedRoundsAfterOrphan int
	for _, p := range pendings[orphanPendingIdx+1:] {
		if len(p.missed) == 0 {
			completedRoundsAfterOrphan++
		}
	}
	if completedRoundsAfterOrphan > 0 {
		logger.Errorf(ctx, "[ChatContextManager] ANOMALY: mid-orphan tail: cutPos=%d has %d completed tool-call rounds after it; truncating them too. This should not occur in normal flows — investigate upstream writer.", cutPos, completedRoundsAfterOrphan)
	}

	kept := msgs[:cutPos]
	cutCount := len(msgs) - cutPos
	logger.Warnf(ctx, "[ChatContextManager] truncate orphaned tail: startIdx=%d cutPos=%d dropped=%d", startIdx, cutPos, cutCount)

	// ReplaceMessages writes via AtomicWriteFile (temp + rename + fsync),
	// so the on-disk file either stays at the pre-truncate state or
	// advances to the truncated state. No half-written intermediate is
	// observable by a subsequent read.
	if err := tx.ReplaceMessages(ctx, kept); err != nil {
		return false, fmt.Errorf("replace messages for truncate failed: %w", err)
	}

	return true, nil
}
