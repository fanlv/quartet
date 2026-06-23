package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type ScheduledTask struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
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
}

func NewScheduleID() string {
	t := time.Now()
	var buf [4]byte
	rand.Read(buf[:])
	return fmt.Sprintf("sch-%s-%06d-%s", t.Format("20060102-150405"), t.Nanosecond()/1000, hex.EncodeToString(buf[:]))
}
