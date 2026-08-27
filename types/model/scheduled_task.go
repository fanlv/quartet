package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type ScheduledTask struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Enabled is the effective activation state on the current machine.
	Enabled   bool      `json:"enabled"`
	CronExpr  string    `json:"cronExpr"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// GraphWorkflowID references the saved Graph workflow this schedule runs.
	// The trigger re-reads the live workflow at run time, so workflow schedules
	// keep no config snapshot.
	GraphWorkflowID string `json:"graphWorkflowId,omitempty"`

	// Workspace & working directory. WorkspaceID is optional: scheduled tasks
	// are conceptually independent automation rules and do not have to belong
	// to a workspace. Empty means the trigger falls back to the default
	// workspace at run time.
	WorkspaceID string `json:"workspaceId,omitempty"`
	Workdir     string `json:"workdir,omitempty"`

	// Execution policy
	MaxConcurrent int `json:"maxConcurrent,omitempty"` // default 1: skip trigger if previous run still active
	Timeout       int `json:"timeout,omitempty"`       // minutes, 0 = unlimited

	// Run status (updated by scheduler)
	LastRunAt    *time.Time `json:"lastRunAt,omitempty"`
	LastRunJobID string     `json:"lastRunJobID,omitempty"`
	LastStatus   JobStatus  `json:"lastStatus,omitempty"`
	// LastTriggerError stores the full error text for failures that happen while
	// trying to create/start the scheduled run, before the run can report its own
	// terminal error.
	LastTriggerError string     `json:"lastTriggerError,omitempty"`
	NextRunAt        *time.Time `json:"nextRunAt,omitempty"`
	RunCount         int        `json:"runCount"`
	StateUpdatedAt   time.Time  `json:"-"`
}

// ScheduleDefinition is the human-maintained portion stored under
// quartet/config/schedules. Scheduler executions never modify this shape.
type ScheduleDefinition struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	CronExpr        string    `json:"cronExpr"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	GraphWorkflowID string    `json:"graphWorkflowId,omitempty"`
	WorkspaceID     string    `json:"workspaceId,omitempty"`
	Workdir         string    `json:"workdir,omitempty"`
	MaxConcurrent   int       `json:"maxConcurrent,omitempty"`
	Timeout         int       `json:"timeout,omitempty"`
}

// ScheduleState is the machine-local portion stored under
// var/quartet/state/schedules. Enabled deliberately lives here instead of in
// the Git-managed definition so each machine can activate the same schedule
// independently. A missing state file means disabled and not run yet.
type ScheduleState struct {
	ID               string     `json:"id"`
	Enabled          bool       `json:"enabled"`
	LastRunAt        *time.Time `json:"lastRunAt,omitempty"`
	LastRunJobID     string     `json:"lastRunJobID,omitempty"`
	LastStatus       JobStatus  `json:"lastStatus,omitempty"`
	LastTriggerError string     `json:"lastTriggerError,omitempty"`
	NextRunAt        *time.Time `json:"nextRunAt,omitempty"`
	RunCount         int        `json:"runCount"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func NewScheduleID() string {
	t := time.Now()
	var buf [4]byte
	rand.Read(buf[:])
	return fmt.Sprintf("sch-%s-%06d-%s", t.Format("20060102-150405"), t.Nanosecond()/1000, hex.EncodeToString(buf[:]))
}
