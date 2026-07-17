package model

type EventType string

const (
	EventTypeTextMessageStart   EventType = "TEXT_MESSAGE_START"
	EventTypeTextMessageContent EventType = "TEXT_MESSAGE_CONTENT"
	EventTypeTextMessageEnd     EventType = "TEXT_MESSAGE_END"
	EventTypeToolCallStart      EventType = "TOOL_CALL_START"
	EventTypeToolCallArgs       EventType = "TOOL_CALL_ARGS"
	EventTypeToolCallResult     EventType = "TOOL_CALL_RESULT"
	EventTypeToolCallEnd        EventType = "TOOL_CALL_END"
	// EventTypeToolCallStitched is emitted when a tool call's terminal
	// status arrives AFTER the round was eagerly flushed and the bubble
	// closed as Placeholder. The payload carries the real content and
	// final status so the live UI can rewrite the bubble in place; the
	// frontend must accept this event even when the message is already
	// in the Finished state (Placeholder), unlike TOOL_CALL_RESULT /
	// TOOL_CALL_END which are gated against late updates.
	EventTypeToolCallStitched EventType = "TOOL_CALL_STITCHED"
	EventTypeCustom             EventType = "CUSTOM"
	EventTypeRunStarted         EventType = "RUN_STARTED"
	EventTypeRunFinished        EventType = "RUN_FINISHED"
	EventTypeRunError           EventType = "RUN_ERROR"

	// Job-level events
	EventTypeJobStarted         EventType = "JOB_STARTED"
	EventTypeJobCompleted       EventType = "JOB_COMPLETED"
	EventTypeJobStopped         EventType = "JOB_STOPPED"
	EventTypeJobFailed          EventType = "JOB_FAILED"
	EventTypeIterationStarted   EventType = "ITERATION_STARTED"
	EventTypeIterationCompleted EventType = "ITERATION_COMPLETED"
	EventTypeIterationFailed    EventType = "ITERATION_FAILED"

	// Command feedback — pushed by the chat-page message handler after a
	// slash command (e.g. /help, /ws N) runs successfully. Transient: not
	// persisted to the Job's message history, so refreshing the page drops
	// it.
	EventTypeCommandSystemMessage EventType = "COMMAND_SYSTEM_MESSAGE"
)

type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleSystem    MessageRole = "system"
	MessageRoleTool      MessageRole = "tool"
	MessageRoleCustom    MessageRole = "custom"
)

type ToolCallStatus string

const (
	ToolCallStatusProcessing  ToolCallStatus = "Processing"
	ToolCallStatusSuccess     ToolCallStatus = "Success"
	ToolCallStatusError       ToolCallStatus = "Error"
	// ToolCallStatusPlaceholder marks the bubble as interrupted /
	// superseded — the surrounding run ended before the tool ever
	// produced a real terminal status. Visually distinct from Error
	// (which signals a real failure) and matches the placeholder flag
	// written to history, so live UI and reload stay consistent.
	ToolCallStatusPlaceholder ToolCallStatus = "Placeholder"
)

type BaseEvent struct {
	Type      EventType      `json:"type"`
	SessionID string         `json:"sessionId"`
	RunID     string         `json:"runId"`
	StepID    string         `json:"stepId,omitempty"`
	Timestamp int64          `json:"timestamp"`
	External  map[string]any `json:"external,omitempty"`

	// Loop context (only set in loop mode)
	JobID string `json:"jobId,omitempty"`
	Path  []int  `json:"path,omitempty"`
}

type RunStartedEvent struct {
	BaseEvent
}

type TokenUsage struct {
	TotalTokens int `json:"totalTokens"`
}

type RunFinishedEvent struct {
	BaseEvent
}

type RunErrorEvent struct {
	BaseEvent
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type TextMessageStartEvent struct {
	BaseEvent
	MessageID   string      `json:"messageId"`
	Role        MessageRole `json:"role"`
	Name        string      `json:"name,omitempty"`
	Description string      `json:"description,omitempty"`
}

type TextMessageContentEvent struct {
	BaseEvent
	MessageID   string      `json:"messageId"`
	Role        MessageRole `json:"role"`
	Name        string      `json:"name,omitempty"`
	Description string      `json:"description,omitempty"`
	Delta       string      `json:"delta"`
}

type TextMessageEndEvent struct {
	BaseEvent
	MessageID   string      `json:"messageId"`
	Role        MessageRole `json:"role"`
	Name        string      `json:"name,omitempty"`
	Description string      `json:"description,omitempty"`
}

type ToolCallStartEvent struct {
	BaseEvent
	ToolCallID      string         `json:"toolCallId"`
	ToolCallName    string         `json:"toolCallName"`
	ParentMessageID string         `json:"parentMessageId,omitempty"`
	ToolCallStatus  ToolCallStatus `json:"toolCallStatus,omitempty"`
}

type ToolCallArgsEvent struct {
	BaseEvent
	ToolCallID      string         `json:"toolCallId"`
	ToolCallName    string         `json:"toolCallName,omitempty"`
	ParentMessageID string         `json:"parentMessageId,omitempty"`
	Delta           string         `json:"delta"`
	ToolCallStatus  ToolCallStatus `json:"toolCallStatus,omitempty"`
}

type ToolCallResultEvent struct {
	BaseEvent
	ToolCallID      string         `json:"toolCallId"`
	ToolCallName    string         `json:"toolCallName,omitempty"`
	ParentMessageID string         `json:"parentMessageId,omitempty"`
	Delta           string         `json:"delta"`
	ToolCallStatus  ToolCallStatus `json:"toolCallStatus"`
}

type ToolCallEndEvent struct {
	BaseEvent
	ToolCallID      string         `json:"toolCallId"`
	ToolCallName    string         `json:"toolCallName,omitempty"`
	ParentMessageID string         `json:"parentMessageId,omitempty"`
	ToolCallStatus  ToolCallStatus `json:"toolCallStatus,omitempty"`
}

// ToolCallStitchedEvent rewrites a Placeholder tool bubble with the real
// terminal payload after a late-arriving terminal. The frontend handles
// this event by replacing the bubble's content and flipping
// toolCallStatus to Success/Error even if the message is already
// Finished. SupersededAgoMs is the gap between the eager flush that
// turned the bubble into a Placeholder and the late terminal that
// produced this stitch — useful for observability tooltips.
type ToolCallStitchedEvent struct {
	BaseEvent
	ToolCallID      string         `json:"toolCallId"`
	ToolCallName    string         `json:"toolCallName,omitempty"`
	ParentMessageID string         `json:"parentMessageId,omitempty"`
	Delta           string         `json:"delta"`
	ToolCallStatus  ToolCallStatus `json:"toolCallStatus"`
	SupersededAgoMs int64          `json:"supersededAgoMs,omitempty"`
}

type CustomEvent struct {
	BaseEvent
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// Agent phase custom event — a transient, non-persisted progress hint
// published while an agent run is in the "silent" preparation window
// (before the first message/thought/tool event reaches the UI). The
// frontend maps these phases to the loading-indicator label so the user
// sees what the agent is doing instead of a fixed "thinking..." string.
// Delivered via PublishTransient: it never enters the event buffer and
// vanishes on page refresh, matching its ephemeral "current status"
// semantics.
const CustomNameAgentPhase = "agent_phase"

const (
	// AgentPhaseStarting — a new ACP subprocess is being launched and the
	// session initialized (cold start; skipped when reusing a cached agent).
	AgentPhaseStarting = "starting"
	// AgentPhaseReconnecting — the subprocess died (OOM / crash / idle reap)
	// and is being relaunched, restoring the session.
	AgentPhaseReconnecting = "reconnecting"
	// AgentPhaseThinking — the prompt has been submitted; waiting for the
	// model's first token (TTFT). Maps to the default "thinking" label.
	AgentPhaseThinking = "thinking"
)

// AgentPhaseValue is the payload of a CustomNameAgentPhase event.
type AgentPhaseValue struct {
	Phase  string `json:"phase"`
	Detail string `json:"detail,omitempty"`
}

// CommandSystemMessageEvent carries a slash-command execution result for the
// Web chat page. Includes the rendered message text plus the structured
// Action so the frontend can additionally apply state changes (e.g. switch
// workspace / reload job) on top of rendering the inline bubble.
type CommandSystemMessageEvent struct {
	BaseEvent
	// Command is the canonical command name (e.g. "/help", "/ws").
	Command string `json:"command"`
	// Text is what the user sees.
	Text string `json:"text"`
	// Present is a hint for renderer placement: "inline" (system bubble) or
	// "toast".
	Present string `json:"present,omitempty"`
	// Action mirrors services/command.Action when the command wants the
	// frontend to apply a side effect. Nil for display-only commands (e.g.
	// /help, /status) so the JSON payload drops the field entirely —
	// `omitempty` on a non-pointer struct is a no-op in encoding/json, so
	// without the pointer we'd always emit `"action":{}`.
	Action *CommandAction `json:"action,omitempty"`
}

// CommandAction is the frontend-visible shape of services/command.Action.
// Kept separate so the types/model package stays free of the command service
// dependency.
type CommandAction struct {
	Type        string `json:"type,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	JobID       string `json:"jobId,omitempty"`
}
