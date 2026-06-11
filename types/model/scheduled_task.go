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

	// Reference to the template this schedule was created from (optional).
	// When set, the trigger re-reads the live template at run time, so editing
	// the template changes what this schedule executes on its next run.
	TemplateID string `json:"templateId,omitempty"`

	// Execution configuration. Copied from the template at creation time and
	// used as a fallback snapshot when TemplateID is empty or the live template
	// can no longer be read/validated at trigger time. When TemplateID resolves
	// to a valid template, that live config takes precedence over this snapshot.
	// Agent/model config lives on each FlowNode step within the LoopConfig.
	LoopConfig LoopConfig `json:"loopConfig"`

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
	NextRunAt    *time.Time `json:"nextRunAt,omitempty"`
	RunCount     int        `json:"runCount"`
}

func NewScheduleID() string {
	t := time.Now()
	var buf [4]byte
	rand.Read(buf[:])
	return fmt.Sprintf("sch-%s-%06d-%s", t.Format("20060102-150405"), t.Nanosecond()/1000, hex.EncodeToString(buf[:]))
}
