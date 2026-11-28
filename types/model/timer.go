package model

import "time"

const (
	TimerPrompt = "prompt"
	TimerGraph  = "graph"

	TimerEnabled         = "enabled"
	TimerDisabled        = "disabled"
	TimerEnded           = "ended"
	TimerPendingApproval = "pendingApproval"

	TimerAccepted    = "accepted"
	TimerDispatching = "dispatching"
	TimerLaunched    = "launched"
	TimerInterrupted = "interrupted"
	TimerSkipped     = "skipped"
	TimerMissed      = "missed"
)

// TimerSpec augments the persisted schedule definition. An empty Kind denotes a legacy schedule.
type TimerSpec struct {
	Kind     string `json:"kind,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	AgentID  string `json:"agentId,omitempty"`
	ModelID  string `json:"modelId,omitempty"`
	RunAt    string `json:"runAt,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
	EndDate  string `json:"endDate,omitempty"`
	MaxRuns  int    `json:"maxRuns,omitempty"`
}

// TimerTarget contains the effective parameters captured at admission.
type TimerTarget struct {
	Task          ScheduledTask  `json:"task"`
	Workflow      *GraphWorkflow `json:"workflow,omitempty"`
	AgentRevision string         `json:"agentRevision,omitempty"`
}

type TimerExecution struct {
	ID            string         `json:"id"`
	ScheduleID    string         `json:"scheduleId"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	WorkflowID    string         `json:"workflowId,omitempty"`
	WorkspaceID   string         `json:"workspaceId,omitempty"`
	JobID         string         `json:"jobId,omitempty"`
	Trigger       string         `json:"trigger"`
	ScheduledAt   *time.Time     `json:"scheduledAt,omitempty"`
	MissedThrough *time.Time     `json:"missedThrough,omitempty"`
	TriggeredAt   *time.Time     `json:"triggeredAt,omitempty"`
	StartedAt     *time.Time     `json:"startedAt,omitempty"`
	FinishedAt    *time.Time     `json:"finishedAt,omitempty"`
	Status        string         `json:"status"`
	Error         string         `json:"error,omitempty"`
	DetailsError  string         `json:"detailsError,omitempty"`
	Configuration *ScheduledTask `json:"configuration,omitempty"`
}

type TimerEntry struct {
	TimerExecution
	Target          *TimerTarget `json:"target,omitempty"`
	LastKnownStatus string       `json:"lastKnownStatus,omitempty"`
}

type TimerLedger struct {
	ID             string       `json:"id"`
	Enabled        bool         `json:"enabled"`
	Approval       string       `json:"approval,omitempty"`
	Definition     string       `json:"definition,omitempty"`
	ApprovedTarget *TimerTarget `json:"approvedTarget,omitempty"`
	PendingReason  string       `json:"pendingReason,omitempty"`
	CheckedThrough time.Time    `json:"checkedThrough"`
	PreviousCount  int          `json:"previousCount,omitempty"`
	Entries        []TimerEntry `json:"entries,omitempty"`
}

type TimerSession struct {
	ID           string               `json:"id"`
	ScheduleID   string               `json:"scheduleId"`
	ScheduleName string               `json:"scheduleName"`
	ExecutionID  string               `json:"executionId"`
	JobID        string               `json:"jobId"`
	WorkspaceID  string               `json:"workspaceId"`
	AgentID      string               `json:"agentId,omitempty"`
	ModelID      string               `json:"modelId,omitempty"`
	Title        string               `json:"title"`
	CreatedAt    time.Time            `json:"createdAt"`
	NodeID       string               `json:"nodeId,omitempty"`
	NodeName     string               `json:"nodeName,omitempty"`
	Loop         []GraphLoopIteration `json:"loop,omitempty"`
}
