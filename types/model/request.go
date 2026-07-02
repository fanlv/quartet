package model

type RequestMessage struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Content   string   `json:"content"`
	Timestamp int64    `json:"timestamp"`
	Role      string   `json:"role"`
	ImageUrls []string `json:"imageUrls,omitempty"`
}

type GetPromptRequest struct {
	Key string `json:"key"`
}

type SavePromptRequest struct {
	Key    string `json:"key"`
	Prompt string `json:"prompt"`
}

type CreateJobRequest struct {
	ModelID         string      `json:"modelId"`
	AgentType       string      `json:"agentType"`
	ACPMode         string      `json:"acpMode,omitempty"`
	ACPThoughtLevel string      `json:"acpThoughtLevel,omitempty"`
	Mode            JobMode     `json:"mode,omitempty"`
	Workdir         string      `json:"workdir,omitempty"`
	WorkspaceID     string      `json:"workspaceId"`
	LoopConfig      *LoopConfig `json:"loopConfig,omitempty"`
}

// UpdateLoopConfigRequest carries a loop job's full LoopConfig for editing.
// The client always sends the complete config; the server applies it as a full
// replacement when the job is not running, or as a per-step field update when
// it is running (rejecting structure changes). See JobUpdateLoopConfig.
type UpdateLoopConfigRequest struct {
	LoopConfig *LoopConfig `json:"loopConfig"`
}

type JobMessageRequest struct {
	Messages        []RequestMessage `json:"messages,omitempty"`
	ModelID         string           `json:"modelId,omitempty"`
	AgentType       string           `json:"agentType,omitempty"`
	SessionID       string           `json:"sessionId,omitempty"`
	ClientMessageID string           `json:"clientMessageId,omitempty"`
	ACPMode         string           `json:"acpMode,omitempty"`
	ACPThoughtLevel string           `json:"acpThoughtLevel,omitempty"`
	// BypassCommand forces the message to go through the regular Job message
	// flow even when the text starts with a known slash command. Set by the
	// Web home page when it builds a new Job from the user's first input:
	// the home page is a pure "message" surface and never executes commands.
	BypassCommand bool `json:"bypassCommand,omitempty"`
}

// ACPConfigTarget names which selector an ACP live-config switch changes.
type ACPConfigTarget string

const (
	ACPConfigTargetModel        ACPConfigTarget = "model"
	ACPConfigTargetMode         ACPConfigTarget = "mode"
	ACPConfigTargetThoughtLevel ACPConfigTarget = "thoughtLevel"
)

// SetACPConfigRequest switches an ACP selector (model / mode / thought_level)
// and asks for the refreshed selector lists back. When SessionID is set the
// switch applies to that session's live agent; otherwise it is a Home
// (session-less) preview that spins up a throwaway session on AgentType.
//
// Model / Mode / ThoughtLevel carry the full current selection so the Home
// preview can replay it into a fresh session before reading the linked lists
// back. For the session path only the Target's value is used (the others are
// already the session's persisted state).
type SetACPConfigRequest struct {
	SessionID    string          `json:"sessionId,omitempty"`
	AgentType    string          `json:"agentType,omitempty"`
	Target       ACPConfigTarget `json:"target"`
	Model        string          `json:"model,omitempty"`
	Mode         string          `json:"mode,omitempty"`
	ThoughtLevel string          `json:"thoughtLevel,omitempty"`
}

// SetACPConfigResponse returns the refreshed selector lists after a switch.
// Each list is populated only when the ACP response carried a refreshed list
// for it (see ACPConfigState); the frontend keeps its current values for nil
// lists.
type SetACPConfigResponse struct {
	Code int `json:"code"`
	ACPConfigState
}

type CreateWorkspaceRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Workdir     string `json:"workdir"`
}

type UpdateWorkspaceRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Workdir     string `json:"workdir"`
}

// ---- Scheduled Task requests ----

type CreateScheduleRequest struct {
	Name            string `json:"name"`
	CronExpr        string `json:"cronExpr"`
	GraphWorkflowID string `json:"graphWorkflowId,omitempty"`
	WorkspaceID     string `json:"workspaceId,omitempty"`
	Workdir         string `json:"workdir,omitempty"`
	MaxConcurrent   int    `json:"maxConcurrent,omitempty"`
	Timeout         int    `json:"timeout,omitempty"`
	// Enabled lets the caller opt out of activating the schedule on create.
	// nil keeps the historical "enabled by default" behavior.
	Enabled *bool `json:"enabled,omitempty"`
}

type UpdateScheduleRequest struct {
	Name            *string `json:"name,omitempty"`
	CronExpr        *string `json:"cronExpr,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty"`
	GraphWorkflowID *string `json:"graphWorkflowId,omitempty"`
	WorkspaceID     *string `json:"workspaceId,omitempty"`
	Workdir         *string `json:"workdir,omitempty"`
	MaxConcurrent   *int    `json:"maxConcurrent,omitempty"`
	Timeout         *int    `json:"timeout,omitempty"`
}
