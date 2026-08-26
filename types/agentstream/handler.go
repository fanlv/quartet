// Package agentstream defines the protocol-neutral streaming event contract
// shared by every agent path (acp today; future integrations). Each
// path's event source translates its native events into StreamHandler method
// calls; services/agent/round.Builder implements StreamHandler to aggregate
// chunks into rounds and forward UI events via types/agui.EventHandler.
//
// This package depends only on eino/schema (message model) and has no
// awareness of acp or agui.
package agentstream

// ToolCallStatus mirrors the subset of tool-call status values that the
// streaming layer cares about. Terminal statuses (Completed / Failed) signal
// that a tool call has produced its final result; non-terminal statuses
// (Pending / InProgress) may still carry argument deltas but no result.
type ToolCallStatus int

const (
	ToolCallStatusPending ToolCallStatus = iota
	ToolCallStatusInProgress
	ToolCallStatusCompleted
	ToolCallStatusFailed
)

// IsTerminal reports whether the status represents a final outcome that
// should trigger the streaming layer to append a tool result.
func (s ToolCallStatus) IsTerminal() bool {
	return s == ToolCallStatusCompleted || s == ToolCallStatusFailed
}

// StreamHandler consumes chunk-level streaming events from an agent path.
// Implementations are expected to be safe for concurrent invocation from the
// path's read goroutine (builders typically self-lock).
//
// Method semantics:
//   - OnMessageChunk: append an assistant message text fragment. A new
//     chunk after tool calls have accumulated signals the previous round is
//     complete and should be flushed.
//   - OnThoughtChunk: append a reasoning/thought text fragment. Same
//     round-boundary semantics as OnMessageChunk.
//   - OnToolCall: declare a new tool call with id+title. Subsequent
//     OnToolCallUpdate / OnToolCallArgsSnapshot calls with the same id carry
//     arguments and the terminal result.
//   - OnToolCallUpdate: either an argument delta (non-terminal, content is
//     an incremental args chunk) or the terminal result (terminal, content
//     is the final result text). The distinction is carried by status.
//   - OnToolCallArgsSnapshot: replace the tool call's accumulated arguments
//     with a full snapshot. ACP rawInput updates use replacement semantics
//     rather than incremental deltas.
//   - OnTokenUsage: current context-window occupancy gauge (optional). It is
//     useful for live UI and as an estimate, but is not provider billing usage.
type StreamHandler interface {
	OnMessageChunk(text string)
	OnThoughtChunk(text string)
	OnToolCall(id, title string)
	OnToolCallUpdate(id, content string, status ToolCallStatus)
	OnToolCallArgsSnapshot(id, args string)
	OnTokenUsage(totalTokens int)
}
