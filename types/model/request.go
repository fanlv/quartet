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
	ModelID     string      `json:"modelId"`
	AgentType   string      `json:"agentType"`
	ACPMode     string      `json:"acpMode,omitempty"`
	Mode        JobMode     `json:"mode,omitempty"`
	Workdir     string      `json:"workdir,omitempty"`
	WorkspaceID string      `json:"workspaceId"`
	LoopConfig  *LoopConfig `json:"loopConfig,omitempty"`
}

type JobMessageRequest struct {
	Messages        []RequestMessage `json:"messages,omitempty"`
	ModelID         string           `json:"modelId,omitempty"`
	AgentType       string           `json:"agentType,omitempty"`
	SessionID       string           `json:"sessionId,omitempty"`
	ClientMessageID string           `json:"clientMessageId,omitempty"`
	ACPMode         string           `json:"acpMode,omitempty"`
	// BypassCommand forces the message to go through the regular Job message
	// flow even when the text starts with a known slash command. Set by the
	// Web home page when it builds a new Job from the user's first input:
	// the home page is a pure "message" surface and never executes commands.
	BypassCommand bool `json:"bypassCommand,omitempty"`
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
	Name          string     `json:"name"`
	CronExpr      string     `json:"cronExpr"`
	TemplateID    string     `json:"templateId,omitempty"`
	LoopConfig    LoopConfig `json:"loopConfig"`
	WorkspaceID   string     `json:"workspaceId,omitempty"`
	Workdir       string     `json:"workdir,omitempty"`
	MaxConcurrent int        `json:"maxConcurrent,omitempty"`
	Timeout       int        `json:"timeout,omitempty"`
	// Enabled lets the caller opt out of activating the schedule on create.
	// nil keeps the historical "enabled by default" behavior.
	Enabled *bool `json:"enabled,omitempty"`
}

type UpdateScheduleRequest struct {
	Name          *string     `json:"name,omitempty"`
	CronExpr      *string     `json:"cronExpr,omitempty"`
	Enabled       *bool       `json:"enabled,omitempty"`
	TemplateID    *string     `json:"templateId,omitempty"`
	LoopConfig    *LoopConfig `json:"loopConfig,omitempty"`
	MaxConcurrent *int        `json:"maxConcurrent,omitempty"`
	Timeout       *int        `json:"timeout,omitempty"`
}
