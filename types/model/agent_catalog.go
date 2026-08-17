package model

const AgentCatalogVersion = 1

type AgentCatalogSource string

const (
	AgentCatalogSourceBuiltin AgentCatalogSource = "builtin"
	AgentCatalogSourceCustom  AgentCatalogSource = "custom"
)

type AgentLifecycle string

const (
	AgentLifecycleActive   AgentLifecycle = "active"
	AgentLifecycleDeleting AgentLifecycle = "deleting"
	AgentLifecycleDeleted  AgentLifecycle = "deleted"
)

// AgentRuntimeDefinition is the structured, shell-free command used to start
// an ACP server. Args are ordered and each element is passed as one argv value.
type AgentRuntimeDefinition struct {
	Bin        string   `json:"bin"`
	ACPProgram string   `json:"acp_program"`
	ACPArgs    []string `json:"acp_args"`
}

type AgentRuntimeBinding struct {
	AgentID    string                 `json:"agent_id"`
	Revision   string                 `json:"revision"`
	RuntimeKey string                 `json:"runtime_key"`
	Definition AgentRuntimeDefinition `json:"definition"`
}

// AgentRuntimeRevision is persisted even when it is no longer current if a
// session or unfinished execution still references it.
type AgentRuntimeRevision struct {
	Revision   string                 `json:"revision"`
	Definition AgentRuntimeDefinition `json:"definition"`
}

// CustomAgent is the durable custom portion of the Agent directory. Management
// workflows own validation and revision creation; the catalog preserves their
// result without flattening structured argv values.
type CustomAgent struct {
	AgentID               string                 `json:"agent_id"`
	DisplayName           string                 `json:"display_name"`
	IconURL               string                 `json:"icon_url"`
	SupportsHeadlessPrint bool                   `json:"supports_headless_print"`
	Lifecycle             AgentLifecycle         `json:"lifecycle"`
	DeleteCleanupStarted  bool                   `json:"delete_cleanup_started,omitempty"`
	DeleteError           string                 `json:"delete_error,omitempty"`
	CurrentRevision       string                 `json:"current_revision"`
	Revisions             []AgentRuntimeRevision `json:"revisions"`
}

type AgentCatalogSnapshot struct {
	Version          int                               `json:"version"`
	Agents           []CustomAgent                     `json:"agents"`
	BuiltinRevisions map[string][]AgentRuntimeRevision `json:"builtin_revisions,omitempty"`
}

type AgentCatalogIdentifier struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// AgentCatalogItem is the ordinary management-directory projection. Custom
// historical revisions stay in the persisted record and are returned only by
// the single-Agent detail endpoint.
type AgentCatalogItem struct {
	AgentID               string                   `json:"agent_id"`
	Source                AgentCatalogSource       `json:"source"`
	DisplayName           string                   `json:"display_name"`
	IconURL               string                   `json:"icon_url"`
	Definition            AgentRuntimeDefinition   `json:"definition"`
	HistoricalIdentifiers []AgentCatalogIdentifier `json:"historical_identifiers,omitempty"`
	SupportsHeadlessPrint bool                     `json:"supports_headless_print"`
	Deprecated            bool                     `json:"deprecated"`
	Lifecycle             AgentLifecycle           `json:"lifecycle"`
	CurrentRevision       string                   `json:"current_revision,omitempty"`
	InstallMethod         string                   `json:"install_method,omitempty"`
	InstallCommands       []string                 `json:"install_commands,omitempty"`
	InstallInstructions   string                   `json:"install_instructions,omitempty"`
	AutoInstallable       bool                     `json:"auto_installable,omitempty"`
	AutoUninstallable     bool                     `json:"auto_uninstallable,omitempty"`
	Installed             bool                     `json:"installed"`
	Availability          string                   `json:"availability"`
	AvailabilityError     string                   `json:"availability_error,omitempty"`
	LastValidationStatus  string                   `json:"last_validation_status,omitempty"`
	LastValidationError   string                   `json:"last_validation_error,omitempty"`
	LastValidationAt      int64                    `json:"last_validation_at,omitempty"`
	DeleteError           string                   `json:"delete_error,omitempty"`
	Refreshing            bool                     `json:"refreshing,omitempty"`
}

type AgentCatalogResponse struct {
	Code   int                `json:"code"`
	Agents []AgentCatalogItem `json:"agents"`
}

type CustomAgentUpsertRequest struct {
	DisplayName           string                 `json:"display_name"`
	IconURL               string                 `json:"icon_url"`
	SupportsHeadlessPrint bool                   `json:"supports_headless_print"`
	Definition            AgentRuntimeDefinition `json:"definition"`
	Environment           []AgentEnvironmentItem `json:"environment,omitempty"`
}

type AgentEnvironmentItem struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type CustomAgentResponse struct {
	Code    int              `json:"code"`
	Agent   AgentCatalogItem `json:"agent"`
	Warning string           `json:"warning,omitempty"`
}

type AgentCatalogDetailResponse struct {
	Code      int                    `json:"code"`
	Agent     AgentCatalogItem       `json:"agent"`
	Revisions []AgentRuntimeRevision `json:"revisions"`
}

type AgentRevalidateRequest struct {
	Revision string `json:"revision,omitempty"`
}

type AgentDeleteRequest struct {
	Force bool `json:"force,omitempty"`
}

type AgentDeleteImpact struct {
	AgentID           string   `json:"agent_id"`
	ClearedSettings   []string `json:"cleared_settings"`
	RetainedWorkflows []string `json:"retained_workflows"`
	RetainedSchedules []string `json:"retained_schedules"`
	RetainedJobs      []string `json:"retained_jobs"`
	RetainedSessions  []string `json:"retained_sessions"`
	BlockingJobIDs    []string `json:"blocking_job_ids"`
}

type AgentDeleteImpactResponse struct {
	Code   int               `json:"code"`
	Impact AgentDeleteImpact `json:"impact"`
}

type AgentDeleteStopResult struct {
	JobID      string `json:"job_id"`
	GraphRunID string `json:"graph_run_id,omitempty"`
	Stopped    bool   `json:"stopped"`
	Error      string `json:"error,omitempty"`
}

type AgentDeleteResult struct {
	Status      string                  `json:"status"`
	StopResults []AgentDeleteStopResult `json:"stop_results,omitempty"`
	Impact      AgentDeleteImpact       `json:"impact"`
}

type AgentDeleteResponse struct {
	Code   int               `json:"code"`
	Result AgentDeleteResult `json:"result"`
}
