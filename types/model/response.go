package model

type SessionInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type ListSessionsResponse struct {
	Sessions []SessionInfo `json:"sessions"`
}

type HistoryMessage struct {
	ID               string         `json:"id"`
	Role             MessageRole    `json:"role"`
	Content          string         `json:"content"`
	ReasoningContent string         `json:"reasoningContent,omitempty"`
	ToolCallID       string         `json:"toolCallId,omitempty"`
	ToolCalls        []ToolCallInfo `json:"toolCalls,omitempty"`
	ImageUrls        []string       `json:"imageUrls,omitempty"`
	IsShellOutput    bool           `json:"isShellOutput,omitempty"`
	IsThinking       bool           `json:"isThinking,omitempty"`
	// Failed is true for role=tool messages whose stored content carries
	// the "[failed] " prefix. The server detects the prefix once and
	// surfaces this flag so the frontend can paint the tool bubble with
	// the correct error status on history reload without re-parsing the
	// content itself.
	Failed bool `json:"failed,omitempty"`
	// Placeholder is true for role=tool messages the round builder
	// synthesised as a placeholder when a round was flushed without its
	// tool result arriving (cancellation / interruption / superseded).
	// Placeholders have the stored content "[placeholder] <reason>" and
	// are NOT "[failed]". Separate from Failed so the frontend can pick
	// a distinct icon / tooltip — rendering a placeholder as a green
	// "completed" bubble (the pre-fix behaviour) contradicts what the
	// user last saw on-screen before the run was interrupted.
	Placeholder       bool   `json:"placeholder,omitempty"`
	PlaceholderReason string `json:"placeholderReason,omitempty"`
	// Duration tracking timestamps (unix millis)
	StartedAt         int64 `json:"startedAt,omitempty"`
	FinishedAt        int64 `json:"finishedAt,omitempty"`
	ThoughtStartedAt  int64 `json:"thoughtStartedAt,omitempty"`
	ThoughtFinishedAt int64 `json:"thoughtFinishedAt,omitempty"`
}

type ToolCallInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type GetMessagesResponse struct {
	ModelID         string           `json:"modelId"`
	Type            string           `json:"type,omitempty"`
	Messages        []HistoryMessage `json:"messages"`
	TokenUsage      *TokenUsage      `json:"tokenUsage,omitempty"`
	Workdir         string           `json:"workdir,omitempty"`
	ACPMode         string           `json:"acpMode,omitempty"`
	ACPThoughtLevel string           `json:"acpThoughtLevel,omitempty"`
}

type GetPromptResponse struct {
	Code   int    `json:"code"`
	Prompt string `json:"prompt"`
	// Path is the on-disk source file for file-backed prompt keys (SOUL /
	// USER / MEMORY). Empty when the key is stored in the prompt DB.
	Path string `json:"path,omitempty"`
}

type SavePromptResponse struct {
	Code int `json:"code"`
}

type AgentInfo struct {
	Type          string                    `json:"type"`
	EnvKey        string                    `json:"env_key,omitempty"`
	ModelID       string                    `json:"model_id"`
	DisplayName   string                    `json:"display_name"`
	IconURL       string                    `json:"icon_url"`
	Models        *SessionModelState        `json:"models,omitempty"`
	Modes         *SessionModeState         `json:"modes,omitempty"`
	ThoughtLevels *SessionThoughtLevelState `json:"thoughtLevels,omitempty"`
}

type SessionModelState struct {
	AvailableModels []ModelInfoACP `json:"availableModels"`
	CurrentModelId  string         `json:"currentModelId"`
}

type ModelInfoACP struct {
	Description *string `json:"description,omitempty"`
	ModelId     string  `json:"modelId"`
	Name        string  `json:"name"`
}

type SessionModeState struct {
	AvailableModes []ACPSessionMode `json:"availableModes"`
	CurrentModeId  string           `json:"currentModeId"`
}

type ACPSessionMode struct {
	Description *string `json:"description,omitempty"`
	Id          string  `json:"id"`
	Name        string  `json:"name"`
}

type SessionThoughtLevelState struct {
	AvailableThoughtLevels []ACPThoughtLevel `json:"availableThoughtLevels"`
	CurrentThoughtLevelId  string            `json:"currentThoughtLevelId"`
	// ConfigId is the ACP config option id used to set this value (e.g.
	// "reasoning_effort"). Unlike mode, thought_level has no dedicated RPC,
	// so the setter goes through SetSessionConfigOption with this id.
	ConfigId string `json:"configId,omitempty"`
}

type ACPThoughtLevel struct {
	Description *string `json:"description,omitempty"`
	Id          string  `json:"id"`
	Name        string  `json:"name"`
}

const ACPProbeCacheVersion = 1

// ACPProbeCacheEntry is the persisted selector state for one ACP command.
type ACPProbeCacheEntry struct {
	Models        *SessionModelState        `json:"models,omitempty"`
	Modes         *SessionModeState         `json:"modes,omitempty"`
	ThoughtLevels *SessionThoughtLevelState `json:"thoughtLevels,omitempty"`
}

// ACPProbeCacheSnapshot is the last complete in-memory ACP cache written to
// disk. RefreshedAt is Unix milliseconds and controls the disk write interval.
type ACPProbeCacheSnapshot struct {
	Version     int                           `json:"version"`
	RefreshedAt int64                         `json:"refreshed_at"`
	Entries     map[string]ACPProbeCacheEntry `json:"entries"`
}

// ACPConfigState is the response of an ACP live-config switch. Each selector
// list is populated only when the underlying ACP response carried a refreshed
// list for it: switching model or thought_level returns the full linked
// ConfigOptions (so both may refresh together), while switching mode carries
// no ConfigOptions and leaves all three nil. The frontend refreshes the
// non-nil lists and keeps its current values for the nil ones.
type ACPConfigState struct {
	Models        *SessionModelState        `json:"models,omitempty"`
	Modes         *SessionModeState         `json:"modes,omitempty"`
	ThoughtLevels *SessionThoughtLevelState `json:"thoughtLevels,omitempty"`
}

type AgentListResponse struct {
	Code      int         `json:"code"`
	AgentList []AgentInfo `json:"agent_list"`
	Workdir   string      `json:"workdir"`
	JobEnable bool        `json:"job_enable"`
}

type CreateJobResponse struct {
	JobID     string `json:"jobId"`
	CreatedAt int64  `json:"createdAt"`
}

// JobSummary is the compact DTO used by the job list endpoint. It deliberately
// excludes the job's large nested payloads (config trees, per-step result
// arrays, which can each be hundreds of KB on long-lived jobs) so the list
// stays cheap to serialize and transfer. Callers that need those fields must
// hit the detail endpoint (GET /api/v1/job/:jobId) which returns the full Job.
type JobSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	ModelID      string    `json:"modelId,omitempty"`
	Status       JobStatus `json:"status"`
	Mode         JobMode   `json:"mode"`
	WorkspaceID  string    `json:"workspaceId,omitempty"`
	Workdir      string    `json:"workdir,omitempty"`
	CreatedAt    int64     `json:"createdAt"`
	UpdatedAt    int64     `json:"updatedAt"`
	PinnedAt     int64     `json:"pinnedAt,omitempty"`
	SessionCount int       `json:"sessionCount"`
	ScheduleID   string    `json:"scheduleId,omitempty"`
	ShareToken   string    `json:"shareToken,omitempty"`
}

type ListJobsResponse struct {
	Jobs       []JobSummary `json:"jobs"`
	NextCursor string       `json:"nextCursor,omitempty"`
	HasMore    bool         `json:"hasMore"`
	Version    int64        `json:"version,omitempty"`
	// DailyStats is keyed by YYYY-MM-DD and carries the per-day total of
	// duration (ms) and turn count for the workspace this list belongs
	// to. Only the days that appear in the current page are populated;
	// days with no data are absent (the frontend treats absence as "no
	// statistics").
	DailyStats map[string]DailyStatsEntry `json:"dailyStats,omitempty"`
}

// DailyStatsEntry is the per-day total exposed alongside the job list.
// Kept intentionally small so it can be sent on every list request
// without bloating the response.
type DailyStatsEntry struct {
	TotalMs   int64 `json:"totalMs"`
	TurnCount int   `json:"turnCount"`
}

type WorkspaceInfo struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Workdir     string `json:"workdir"`
	Color       string `json:"color,omitempty"`
	Favorite    bool   `json:"favorite"`
	SortOrder   int    `json:"sortOrder"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type ListWorkspacesResponse struct {
	Workspaces []WorkspaceInfo `json:"workspaces"`
}

// ---- Scheduled Task responses ----

type ScheduleInfo struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Enabled          bool      `json:"enabled"`
	CronExpr         string    `json:"cronExpr"`
	GraphWorkflowID  string    `json:"graphWorkflowId,omitempty"`
	WorkspaceID      string    `json:"workspaceId,omitempty"`
	Workdir          string    `json:"workdir,omitempty"`
	MaxConcurrent    int       `json:"maxConcurrent,omitempty"`
	Timeout          int       `json:"timeout,omitempty"`
	LastRunAt        *int64    `json:"lastRunAt,omitempty"`
	LastRunJobID     string    `json:"lastRunJobID,omitempty"`
	LastStatus       JobStatus `json:"lastStatus,omitempty"`
	LastTriggerError string    `json:"lastTriggerError,omitempty"`
	NextRunAt        *int64    `json:"nextRunAt,omitempty"`
	RunCount         int       `json:"runCount"`
	CreatedAt        int64     `json:"createdAt"`
	UpdatedAt        int64     `json:"updatedAt"`
}

type CreateScheduleResponse struct {
	Schedule ScheduleInfo `json:"schedule"`
}

type ListSchedulesResponse struct {
	Schedules []ScheduleInfo `json:"schedules"`
}
