// Package msgextra centralises the well-known keys stored on
// schema.Message.Extra. Keeping them as constants (rather than scattered
// string literals) lets us spot naming collisions at one site when adding
// new keys and prevents drift between writers and readers.
//
// Scope: keys on schema.Message.Extra only. Runtime-only event payload
// fields (e.g. agui event.External) live in their own namespace and are
// intentionally NOT mirrored here even when they share a name.
package msgextra

const (
	// KeyMsgID is the SSE message id the UI handler assigns on
	// OnMessageStart. Written by the round builder onto assistant messages
	// so that reload-from-disk history can reuse the same id the frontend
	// already rendered from the live SSE stream.
	KeyMsgID = "msg_id"

	// KeyThoughtMsgID is the SSE message id the UI handler assigns on
	// OnThoughtStart. Semantically independent from KeyMsgID but stored on
	// the same assistant message (thought + content share one message).
	KeyThoughtMsgID = "thought_msg_id"

	// KeyShellOutput marks a message as shell executor output. The shell
	// executor writes this directly via AppendMessages on both the script
	// user message and the stdout assistant message; the web session
	// handler renders flagged messages with a shell-output bubble.
	KeyShellOutput = "shellOutput"

	// KeyPrePersisted is an in-memory-only marker set on a user message that
	// has ALREADY been appended to messages.jsonl by an upstream caller
	// (graph Prompt/评估 nodes persist the rendered prompt at enqueue time so
	// the Chat sidebar shows it before the agent replies — see
	// services/graph/runtime.go executeNode). When ChatContextManager.BeginRun
	// sees every user message carrying this marker it SKIPS the append (the
	// row is already on disk) while still running the orphan-tail truncate, so
	// the message is written exactly once and never double-rendered. The
	// marked copy is never appended, so this key never reaches disk.
	KeyPrePersisted = "pre_persisted"

	// KeyLocalPath is the local filesystem path for an image attached in a
	// UserInputMultiContent.Image.Extra (not schema.Message.Extra itself —
	// see session handler usage). Kept here as a single source of truth
	// for the key name.
	KeyLocalPath = "local_path"

	// KeyFileSize preserves the uploaded file size on a file input part.
	KeyFileSize = "file_size"

	// KeyFileAttachments preserves structured metadata for ordinary uploaded
	// files while the ACP-compatible prompt carries their readable paths.
	KeyFileAttachments = "file_attachments"

	// KeyOriginalUserContent keeps the user's text separate from the
	// ACP-compatible attachment prefix added to Message.Content.
	KeyOriginalUserContent = "original_user_content"

	// KeyTokenCountCache is the legacy per-message cache used before text and
	// image accounting were separated. Kept so persisted messages still decode.
	KeyTokenCountCache = "_agent_middleware_token_count"

	// KeyTextTokenCountCacheV2 caches text-only tokens. The legacy cache may
	// include generated image-path markup, so image-aware counting must not
	// reuse it before adding the independently calculated image-token subset.
	KeyTextTokenCountCacheV2 = "_agent_middleware_text_token_count_v2"

	// KeyPlaceholderToolResult marks a role=tool message as a synthetic
	// placeholder inserted by the round builder when a round is flushed
	// without all tool results arriving. Placeholders keep
	// tool_use / tool_result paired on disk so the next LLM call does not
	// fail schema validation. Consumers (frontend / debug tools) can use
	// this flag to distinguish placeholders from real tool results.
	KeyPlaceholderToolResult = "placeholder_tool_result"

	// KeyStartedAt is the Unix-millis timestamp when the message/tool-call
	// started (OnMessageStart / OnThoughtStart / OnToolCallStart).
	KeyStartedAt = "started_at"

	// KeyFinishedAt is the Unix-millis timestamp when the message/tool-call
	// ended (OnMessageEnd / OnToolCallEnd).
	KeyFinishedAt = "finished_at"

	// KeyThoughtStartedAt is the Unix-millis timestamp when the thinking
	// phase started (OnThoughtStart). Stored on the assistant message.
	KeyThoughtStartedAt = "thought_started_at"

	// KeyThoughtFinishedAt is the Unix-millis timestamp when the thinking
	// phase ended (first non-thinking content or OnThoughtEnd).
	KeyThoughtFinishedAt = "thought_finished_at"
)

// Placeholder content prefix and reason constants. Full placeholder content
// is PlaceholderPrefix + " " + reason (+ optional free text). Kept
// deliberately distinct from the "[failed]" prefix that real failed tool
// results use, so that LLM-side heuristics that parse the raw content can
// tell placeholders from genuine tool failures.
const (
	PlaceholderPrefix = "[placeholder]"

	// PlaceholderReasonCanceled means the run was cancelled (user interrupt
	// / context cancel) before the tool produced a result.
	PlaceholderReasonCanceled = "canceled"

	// PlaceholderReasonInterrupted means the run exited with an error /
	// exception / unexpected termination before the tool produced a result.
	PlaceholderReasonInterrupted = "interrupted"

	// PlaceholderReasonSuperseded means an eager flush was triggered by
	// the next assistant round (new message / thought chunk) while prior
	// tool calls were still pending. Rare in practice — normally tool
	// results complete before the next round starts.
	PlaceholderReasonSuperseded = "superseded"
)

// FailedPrefix is the content prefix applied by the round builder to
// terminal=Failed tool results before they land both on disk (so the next
// LLM round sees the failure) and on the wire to the UI handler (so live
// rendering matches history reload). Writers and readers MUST use this
// constant rather than the literal so the two sides cannot drift apart.
const FailedPrefix = "[failed] "
