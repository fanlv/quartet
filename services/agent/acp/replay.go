package acp

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/msgextra"
)

// replayHeader is the instruction that opens a replayed conversation
// history block. It tells the model the wrapped lines are prior context
// (not a fresh user turn) and scopes where the current turn actually
// begins, so the model does not re-answer older turns or treat the
// history as a new instruction.
const replayHeader = "The following is prior conversation between the user and the assistant, provided as context after an internal session refresh. " +
	"Treat it as read-only context — do not re-respond to these turns, and do not acknowledge the refresh. " +
	"Respond only to the user message that appears AFTER the closing </conversation-history> tag."

// trimTrailingUserMessages returns msgs with the final `count` messages
// removed. Used by Run() after BeginRun appends the current turn: the
// replay block must contain only prior history, never the just-appended
// user messages. Guarded against underflow so a miscount surfaces as an
// empty replay rather than a panic.
func trimTrailingUserMessages(msgs []*schema.Message, count int) []*schema.Message {
	if count <= 0 || len(msgs) == 0 {
		return msgs
	}
	if count >= len(msgs) {
		return nil
	}
	return msgs[:len(msgs)-count]
}

// buildReplayPrompt formats prior conversation messages into a text
// block that a fresh ACP subprocess can read as context. Called when
// needReplay is set — i.e. the subprocess session was just minted and
// holds no in-memory history, but messages.jsonl does. The output is
// wrapped in a <conversation-history> ... </conversation-history>
// envelope with an explicit "context only" instruction so the model
// does not re-respond to old turns or treat them as a new prompt.
// Empty input returns "" so callers can unconditionally prepend the
// result to the user prompt.
//
// Total length is bounded along two axes to keep a runaway long history
// from blowing past the model's context window after a drift / truncate /
// cold-start reset — exactly the cases where a long conversation has
// accumulated:
//
//   - Non-summary messages share maxReplayPromptBytes; when over budget
//     the newest are kept and a single
//     "[conversation history truncated for replay]" marker stands in for
//     the dropped older turns.
//   - Each summary is independently bounded by maxReplaySummaryRunes so a
//     pathologically large summary cannot single-handedly dominate the
//     replay prefix.
func buildReplayPrompt(history []*schema.Message) string {
	return buildReplayPromptWithCap(history, maxReplayPromptBytes)
}

// buildReplayPromptWithCap is the size-aware core of buildReplayPrompt.
// Exposed (lowercase, package-internal) so tests can pin the cap to a
// small value without setting environment variables.
//
// Selection happens BEFORE rendering so messages destined to be dropped
// by the cap never pay rendering cost — important on the cold-start /
// drift / truncate path where a long history may need replay and the
// caller is already on a slow path. Order of operations:
//
//  1. Render summaries first. They are always retained (compressed
//     prior context) so dropping them would erase the very signal the
//     cap is trying to preserve.
//  2. Walk newest-first, render each non-summary message lazily, and
//     stop adding once the cap would be exceeded — older non-summary
//     messages past that point are never rendered.
//  3. Emit kept messages in original order, inserting a single
//     truncation marker at the first dropped position so the model
//     sees the gap explicitly.
func buildReplayPromptWithCap(history []*schema.Message, capBytes int) string {
	if len(history) == 0 {
		return ""
	}

	rendered := make([]string, len(history))
	capped := capBytes > 0
	usedBytes := 0

	// Phase 1: summaries are always kept regardless of cap, so render
	// them first and treat their bytes as fixed overhead. A summary that
	// happens to render to "" (corrupt / empty body) just contributes
	// nothing — the slot in `rendered` stays empty and Phase 2 skips it.
	for i, m := range history {
		if !isSummaryMessage(m) {
			continue
		}
		body := formatMessageForReplay(m)
		rendered[i] = body
		usedBytes += len(body)
	}

	// Phase 2: walk newest-first, render each non-summary message
	// lazily, and add it while the budget allows. Once we hit overflow
	// we stop rendering older messages entirely — counting them only by
	// a cheap "would this have produced content" predicate so the
	// dropped-count log stays meaningful without paying for full
	// formatting on the discarded tail.
	overflowed := false
	droppedNonSummary := 0
	for i := len(history) - 1; i >= 0; i-- {
		if rendered[i] != "" {
			continue // already rendered as summary in Phase 1
		}
		if isSummaryMessage(history[i]) {
			continue // summary that rendered to "" — already handled
		}
		if overflowed {
			if mightProduceReplayContent(history[i]) {
				droppedNonSummary++
			}
			continue
		}
		body := formatMessageForReplay(history[i])
		if body == "" {
			continue
		}
		if capped && usedBytes+len(body) > capBytes {
			overflowed = true
			droppedNonSummary++
			continue
		}
		rendered[i] = body
		usedBytes += len(body)
	}

	if overflowed {
		keptCount := 0
		for _, body := range rendered {
			if body != "" {
				keptCount++
			}
		}
		logger.Warnf(context.Background(),
			"[acp] replay prefix truncated to fit cap: capBytes=%d usedBytes=%d keptMsgs=%d droppedMsgs=%d",
			capBytes, usedBytes, keptCount, droppedNonSummary)
	}

	var b strings.Builder
	b.WriteString("<conversation-history>\n")
	b.WriteString(replayHeader)
	b.WriteString("\n\n")
	wroteTruncationMarker := false
	for i, body := range rendered {
		if body != "" {
			b.WriteString(body)
			b.WriteString("\n\n")
			continue
		}
		// rendered[i] is empty either because the message naturally
		// produces no replay output (system / empty content) or because
		// the cap dropped it. Only emit the truncation marker for the
		// latter, so a non-replayable system message in the middle of
		// kept turns does not pretend to be a truncation.
		if !overflowed || wroteTruncationMarker {
			continue
		}
		if !mightProduceReplayContent(history[i]) {
			continue
		}
		b.WriteString(replayTruncatedMarker)
		b.WriteString("\n\n")
		wroteTruncationMarker = true
	}
	b.WriteString("</conversation-history>\n\n")
	return b.String()
}

// mightProduceReplayContent is a cheap structural check: does this
// message look like it would render to a non-empty replay block, without
// actually formatting it? Used so the truncation log and marker
// placement can act on dropped older messages without paying the cost
// of formatMessageForReplay on the discarded tail. Errs on the side of
// "yes" — a false positive means an extra truncation marker for a
// message that would have rendered to "" anyway, which is harmless.
func mightProduceReplayContent(m *schema.Message) bool {
	if m == nil {
		return false
	}
	if m.Role == schema.System {
		return false
	}
	if strings.TrimSpace(m.Content) != "" {
		return true
	}
	if m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
		return true
	}
	if m.Role == schema.User && len(m.UserInputMultiContent) > 0 {
		return true
	}
	return false
}

// maxReplayPromptBytes caps the rendered byte size of non-summary messages
// in the <conversation-history> block. 256 KiB strikes a balance: it covers
// dozens of normal turns even after multi-tool rounds, while keeping the
// replay prefix well under any model's context window so the current user
// turn still has room to breathe. When exceeded, summaries are always
// retained (subject to maxReplaySummaryRunes) and the newest non-summary
// messages are kept until the budget is exhausted.
//
// Note on what this cap covers: the small fixed overhead from the
// <conversation-history> envelope, the replayHeader instruction, and per-
// message "\n\n" separators (~1 KiB total for typical histories) is NOT
// counted against this budget. The 256 KiB headroom dwarfs that overhead,
// so giving the cap a precise per-byte meaning would add complexity for
// no measurable benefit. The summary block is the one input that COULD
// dominate uncontrollably — it is bounded separately by
// maxReplaySummaryRunes.
const maxReplayPromptBytes = 256 * 1024

// maxReplaySummaryRunes caps a single summary block. summarization
// middleware normally enforces something tighter on the write side, but
// the replay path treats summary.json as untrusted (it can be hand-edited
// or corrupted between sessions) so we apply our own ceiling. 64 KiB of
// runes is comfortably larger than any sane summary while still leaving
// room for the rest of the conversation-history block under the 256 KiB
// non-summary budget.
const maxReplaySummaryRunes = 64 * 1024

// replayTruncatedMarker is inserted in place of dropped messages when
// the replay prefix exceeds maxReplayPromptBytes. It deliberately
// announces the gap so the model does not synthesise continuity that
// the dropped turns might contradict.
const replayTruncatedMarker = "[conversation history truncated for replay; older turns omitted to fit context]"

// replayBoundaryEscaper neutralises occurrences of the conversation-history
// boundary tags inside replayed message bodies. replayHeader explicitly
// tells the model the closing </conversation-history> tag marks the
// boundary between read-only context and the live user turn, so any
// historical content carrying a literal closing tag could trick the model
// into treating downstream replay text as a fresh instruction. Tool
// results are the realistic vector — they routinely embed external
// content (web pages, command output, fetched files) that the user did
// not author. Inserting a backslash before the closing angle bracket
// makes the text unambiguously not a tag while keeping it human-readable;
// the opening tag is escaped symmetrically as defense-in-depth.
//
// Applied to the body string returned by formatMessageForReplay, AFTER
// role markers like "[user]\n" / "[tool-result call=X]\n" have been
// composed. The role markers and the envelope tags emitted by
// buildReplayPromptWithCap are static strings we own and are intentionally
// left untouched.
var replayBoundaryEscaper = strings.NewReplacer(
	"</conversation-history>", "</conversation-history\\>",
	"<conversation-history>", "<conversation-history\\>",
)

func escapeReplayBoundary(s string) string {
	if s == "" {
		return s
	}
	return replayBoundaryEscaper.Replace(s)
}

// formatMessageForReplay renders one schema.Message as a single block of
// text for inclusion in a replay prefix. Empty output ("") means the
// message contributes nothing — the caller should skip it. Role coverage:
//
//   - User: content plus any UserInputMultiContent text parts (images
//     cannot cross the replay boundary so they are noted rather than
//     embedded).
//   - Assistant: content; if the message declared ToolCalls, each call
//     is listed with its function name + arguments so a subsequent
//     role=tool result can be tied back to the right call.
//   - Tool: the tool result text, prefixed with the ToolCallID so it
//     pairs with the assistant block above it.
//   - System: dropped. System prompts are configured into the subprocess
//     externally; echoing a synthetic one would conflict with that and
//     risk changing behavior mid-replay.
//
// Messages carrying the summary marker (msgextra.KeyIsSummary = true)
// are rendered as a dedicated "prior-summary" block so the model can
// tell compressed history apart from raw turns.
func formatMessageForReplay(m *schema.Message) string {
	if m == nil {
		return ""
	}
	if isSummaryMessage(m) {
		body := strings.TrimSpace(m.Content)
		if body == "" {
			return ""
		}
		// Bound a pathologically large summary so it cannot single-handedly
		// blow past the conversation-history budget. summarization
		// middleware normally caps this on the write side, but a corrupt
		// or hand-edited summary.json should not be able to crowd out the
		// rest of the replay prefix. The truncation marker is the same
		// "(truncated for replay)" suffix used on tool blocks so the model
		// gets a uniform signal that content was elided.
		body = truncateReplayBlock(body, maxReplaySummaryRunes)
		return "[prior-summary]\n" + escapeReplayBoundary(body)
	}
	switch m.Role {
	case schema.User:
		body := userMessageText(m)
		if body == "" {
			return ""
		}
		return "[user]\n" + escapeReplayBoundary(body)
	case schema.Assistant:
		body := assistantMessageText(m)
		if body == "" {
			return ""
		}
		return "[assistant]\n" + escapeReplayBoundary(body)
	case schema.Tool:
		body := strings.TrimSpace(m.Content)
		if body == "" {
			return ""
		}
		// Guard against unbounded tool output blowing the replay prompt.
		// A single 10MB tool result would dominate the entire prefix
		// and likely exceed the model's context; truncate defensively.
		body = truncateReplayBlock(body, maxReplayToolResultRunes)
		body = escapeReplayBoundary(body)
		if m.ToolCallID != "" {
			return fmt.Sprintf("[tool-result call=%s]\n%s", m.ToolCallID, body)
		}
		return "[tool-result]\n" + body
	case schema.System:
		return ""
	default:
		body := strings.TrimSpace(m.Content)
		if body == "" {
			return ""
		}
		return fmt.Sprintf("[%s]\n%s", m.Role, escapeReplayBoundary(body))
	}
}

// maxReplayToolResultRunes caps the size of any single tool result
// embedded in the replay prefix. Chosen to keep one runaway tool result
// from crowding out the rest of the history without losing the signal
// that a result existed.
const maxReplayToolResultRunes = 4000

func userMessageText(m *schema.Message) string {
	if m.Content != "" {
		return strings.TrimSpace(m.Content)
	}
	// Multimodal user turn: content lives in the parts. Concatenate the
	// text parts; note non-text parts so the model knows something was
	// attached even though we cannot replay the bytes.
	var parts []string
	for _, p := range m.UserInputMultiContent {
		if p.Text != "" {
			parts = append(parts, p.Text)
			continue
		}
		parts = append(parts, "[non-text attachment omitted from replay]")
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func assistantMessageText(m *schema.Message) string {
	var sections []string
	if body := strings.TrimSpace(m.Content); body != "" {
		sections = append(sections, body)
	}
	for _, tc := range m.ToolCalls {
		name := tc.Function.Name
		if name == "" {
			name = "<unnamed>"
		}
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			sections = append(sections, fmt.Sprintf("(called tool %q, id=%s)", name, tc.ID))
			continue
		}
		args = truncateReplayBlock(args, maxReplayToolArgsRunes)
		sections = append(sections, fmt.Sprintf("(called tool %q, id=%s, args=%s)", name, tc.ID, args))
	}
	return strings.Join(sections, "\n")
}

const maxReplayToolArgsRunes = 800

func truncateReplayBlock(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return s
	}
	return string(rs[:maxRunes]) + "\n…(truncated for replay)"
}

func isSummaryMessage(m *schema.Message) bool {
	if m == nil || m.Extra == nil {
		return false
	}
	v, ok := m.Extra[msgextra.KeyIsSummary].(bool)
	return ok && v
}
