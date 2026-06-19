package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/fanlv/quartet/types/consts"
)

type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusStopped   JobStatus = "stopped"
)

type JobMode string

const (
	JobModeInteractive JobMode = "interactive"
	JobModeLoop        JobMode = "loop"
	JobModeGraph       JobMode = "graph"
)

// Job represents a single execution unit (interactive chat or loop workflow).
//
// # Field ownership model
//
// The in-memory *Job pointer is shared between the handler goroutine and the
// runLoop goroutine. To prevent data races, fields are split by ownership
// and ALL access MUST be protected by service.mu:
//
// Immutable after creation (safe to read without lock):
//
//	ID, WorkspaceID, CreatedAt, ScheduleID, TimeoutMinutes
//
// Handler-owned (written by handler through targeted service mutators):
//
//	Title, Mode, Workdir, ShareToken, Deleted, PinnedAt, UpdatedAt
//
// RunLoop-owned (written by runLoop):
//
//	Status, StartedAt, FinishedAt, LoopConfig, GraphRunID, Progress, Resume,
//	SessionIDs
//
// Service-owned denormalized cache (written by targeted service mutator only):
//
//	FirstModelID
//
// Note: LoopConfig.Variables may also be written by handler-side methods
// (e.g. UpdateTitle syncs VarJobTitle) under service.mu. This is safe because
// it's a targeted key update on the internal pointer, not a full-struct merge.
type Job struct {
	// --- Immutable after creation ---
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`

	// --- Handler-owned ---
	Title      string    `json:"title"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Deleted    bool      `json:"deleted,omitempty"`
	PinnedAt   int64     `json:"pinnedAt,omitempty"` // unix ms; 0 means not pinned
	Mode       JobMode   `json:"mode"`
	Workdir    string    `json:"workdir,omitempty"`
	ShareToken string    `json:"shareToken,omitempty"`

	// --- Immutable after creation ---
	WorkspaceID    string `json:"workspaceId"`
	ScheduleID     string `json:"scheduleId,omitempty"`
	TimeoutMinutes int    `json:"timeoutMinutes,omitempty"`

	// --- RunLoop-owned ---
	Status     JobStatus    `json:"status"`
	StartedAt  int64        `json:"startedAt,omitempty"`  // unix ms; set when execution begins
	FinishedAt int64        `json:"finishedAt,omitempty"` // unix ms; set when terminal state reached
	LoopConfig *LoopConfig  `json:"loopConfig,omitempty"`
	GraphRunID string       `json:"graphRunId,omitempty"`
	SessionIDs []string     `json:"sessionIds"`
	Progress   *JobProgress `json:"progress,omitempty"`
	Resume     *JobResume   `json:"resume,omitempty"`

	// LastRunOutcome records the actual outcome of the most recent run
	// (loop iteration or interactive send). For interactive sends on an
	// already-terminal job, job.Status is the restored prior status while
	// LastRunOutcome reflects what actually happened in the send. Frontends
	// that recover from a missed terminal SSE event should use this field
	// instead of job.Status to finalize in-flight UI state.
	LastRunOutcome RunOutcome `json:"lastRunOutcome,omitempty"`

	// FirstModelID caches the ModelID of the first non-deleted session for
	// JobInfo listing. Denormalized on the write path so JobList does not
	// need to open every session file (avoids an O(jobs * sessions) I/O hit
	// on the list endpoint). An empty value means "not cached yet" — the
	// lister may lazily fill it and persist.
	FirstModelID string `json:"firstModelId,omitempty"`
}

type RoundMode string

const (
	RoundModeBeforeRound RoundMode = "beforeRound" // Round前开新session
	RoundModeEachRepeat  RoundMode = "eachRepeat"  // 每轮repeat开新session
	RoundModeNone        RoundMode = "none"        // 不开新session
)

type RoundType string

const (
	RoundTypePrompt RoundType = "prompt"
	RoundTypeShell  RoundType = "shell"
	// RoundTypeEvaluator is a prompt step whose output is interpreted as a loop
	// stop signal: the user's evaluation prompt gets a fixed output protocol
	// appended before sending, and the turn's final assistant text is parsed — a
	// case-insensitive, whitespace-agnostic suffix match on LOOP_DECISION:STOP
	// breaks the enclosing group
	// (equivalent to a Shell STOP_LOOP). It reuses the entire prompt-step
	// execution / progress / resume / usage path; only those two points differ.
	RoundTypeEvaluator RoundType = "evaluator"
)

// FlowNodeType distinguishes leaf steps from container groups in the flow tree.
type FlowNodeType string

const (
	FlowNodeTypeStep  FlowNodeType = "step"
	FlowNodeTypeGroup FlowNodeType = "group"
)

// FlowNode is a recursive tree node representing either a concrete execution
// step (prompt or shell) or a group container that iterates its children.
type FlowNode struct {
	ID    string       `json:"id"`
	Type  FlowNodeType `json:"type"`
	Label string       `json:"label,omitempty"`

	// Step fields (Type == "step")
	Message     string    `json:"message,omitempty"`
	RepeatCount int       `json:"repeatCount,omitempty"`
	RoundMode   RoundMode `json:"roundMode,omitempty"`
	RoundType   RoundType `json:"roundType,omitempty"`

	// Per-step agent/model overrides (only used when RoundMode != RoundModeNone)
	AgentType       string `json:"agentType,omitempty"`
	StepModelID     string `json:"modelId,omitempty"`
	ACPMode         string `json:"acpMode,omitempty"`
	ACPThoughtLevel string `json:"acpThoughtLevel,omitempty"`

	// Group fields (Type == "group")
	IterationCount int        `json:"iterationCount,omitempty"`
	Children       []FlowNode `json:"children,omitempty"`
}

// SessionOverrides carries optional per-step agent/model overrides for session creation.
// Zero/empty values mean "use job-level defaults".
type SessionOverrides struct {
	AgentType       string
	ModelID         string
	ACPMode         string
	ACPThoughtLevel string
}

// LoopConfig defines the execution plan for a loop job.
// The canonical field is Flow (recursive tree). The legacy fields IterationCount
// and Rounds are kept for backward-compatible deserialization of old jobs/templates;
// call MigrateLoopConfig to normalize them into Flow before execution.
// When adding new slice/map/pointer fields, update Job.DeepCopy accordingly.
type LoopConfig struct {
	Flow      []FlowNode        `json:"flow,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`

	// DisabledVars lists user-defined variable keys that are toggled off. A
	// disabled variable keeps its entry in Variables (so the value is preserved
	// across toggles) but renders to an empty string during {{key}} substitution.
	// Runtime builtins (_job_id, _current_time, …) are never listed here.
	DisabledVars []string `json:"disabledVars,omitempty"`

	// Deprecated: legacy flat format, kept for migration only.
	IterationCount int         `json:"iterationCount,omitempty"`
	Rounds         []LoopRound `json:"rounds,omitempty"`
}

// LoopRound is the legacy flat round definition. Kept for backward compatibility.
type LoopRound struct {
	Message     string    `json:"message"`
	RepeatCount int       `json:"repeatCount"`
	RoundMode   RoundMode `json:"roundMode"`
	RoundType   RoundType `json:"roundType,omitempty"`
}

// JobProgress tracks loop execution state persisted on a Job.
// When adding new slice/map/pointer fields, update Job.DeepCopy accordingly.
type JobProgress struct {
	TotalSteps  int   `json:"totalSteps"`
	CurrentPath []int `json:"currentPath,omitempty"`
	// CurrentStartedAt is the unix-ms timestamp of the currently running
	// iteration. It is persisted with CurrentPath before ITERATION_STARTED is
	// published so a page opened long after the start can render live duration
	// from the real step boundary instead of guessing from accumulated results.
	CurrentStartedAt int64             `json:"currentStartedAt,omitempty"`
	CompletedCount   int               `json:"completedCount"`
	FailedCount      int               `json:"failedCount"`
	Results          []IterationResult `json:"results,omitempty"`
	// LastError is the most recent failure reason — either from an iteration
	// failure (captured alongside IterationResult.Error) or a job-level
	// failure (panic / failJob). Persisted so refreshes still surface it.
	LastError string `json:"lastError,omitempty"`
	// PersistWarnings records best-effort persistence failures without clobbering
	// LastError. Persistence warnings describe disk/state divergence, while
	// LastError remains reserved for the user-visible run failure reason.
	PersistWarnings []string `json:"persistWarnings,omitempty"`

	// GroupActualIterations maps a group's dot-joined path (e.g. "0.0") to the
	// number of rounds it actually ran when it stopped early via stepStopLoop
	// (evaluator STOP or Shell STOP_LOOP) before reaching its iterationCount
	// cap. The frontend uses this to recompute the session/step plan denominator
	// so the progress text and bar reflect the real run instead of the static
	// cap. Mirrors the in-memory TotalSteps backfill (executor_loop.go) — like
	// that backfill it is only a display aid, not used for resume.
	GroupActualIterations map[string]int `json:"groupActualIterations,omitempty"`
	// GroupActualLeafCounts maps the same group path to the exact number of leaf
	// steps that actually executed inside the group when it stopped early. This is
	// more precise than GroupActualIterations: it also captures sibling leaves that
	// were skipped after STOP within the final actual iteration, allowing the UI
	// session/step plan to trim the group to the same denominator as the backend.
	// The count is the group's CONSUMED slot prefix: leaves that ran plus leaves
	// skipped for an empty rendered prompt (SkippedPaths) — the UI keeps that
	// prefix, then filters the skipped leaves out of it.
	GroupActualLeafCounts map[string]int `json:"groupActualLeafCounts,omitempty"`

	// SkippedPaths records the leaf slots (dot-joined full step paths, e.g.
	// "0.1.2.0" — iteration and repeat indices included) whose prompt rendered
	// to an empty string and were therefore skipped without running: no
	// session, no round events, no IterationResult. Each skipped slot also
	// decremented TotalSteps exactly once — the set is the idempotency guard
	// for that deduction and the UI's input for filtering skipped leaves out
	// of the session plan. Persisted with the job so refresh / Continue /
	// restart all see the same shrunken plan; never recomputed from current
	// variable values (they may have changed since the skip).
	SkippedPaths map[string]bool `json:"skippedPaths,omitempty"`

	// GracefulStopPending reports that a "stop after step" was requested and
	// not yet consumed at a step boundary. It is a runtime-only view field:
	// the authoritative state lives in the service's in-memory gracefulStops
	// map (it cannot survive a process restart, where the request itself is
	// also lost), so it is synthesized onto the snapshot returned by Get and
	// broadcast via a transient SSE event — never written to disk. This lets a
	// page refresh / second tab still show the pending "keep running" affordance.
	GracefulStopPending bool `json:"gracefulStopPending,omitempty"`
}

type JobResume struct {
	NextPath  []int  `json:"nextPath,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type IterationResult struct {
	Path       []int  `json:"path"`
	SessionID  string `json:"sessionId"`
	Success    bool   `json:"success"`
	DurationMs int64  `json:"durationMs"`
	Tokens     int    `json:"tokens"`
	Error      string `json:"error,omitempty"`
	Content    string `json:"content,omitempty"`
}

func NewJob(workdir, workspaceID string) *Job {
	return &Job{
		ID:          newJobID(),
		Title:       consts.DefaultJobTitle,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Status:      JobStatusPending,
		Mode:        JobModeInteractive,
		WorkspaceID: workspaceID,
		Workdir:     workdir,
	}
}

// DeepCopy returns a deep copy of the Job, safe for use across goroutines.
// When adding new slice/map/pointer fields to Job, update this method accordingly.
func (j *Job) DeepCopy() *Job {
	cp := *j

	// SessionIDs
	if len(j.SessionIDs) > 0 {
		cp.SessionIDs = make([]string, len(j.SessionIDs))
		copy(cp.SessionIDs, j.SessionIDs)
	}

	// LoopConfig
	if j.LoopConfig != nil {
		lcCopy := *j.LoopConfig
		if len(j.LoopConfig.Flow) > 0 {
			lcCopy.Flow = DeepCopyFlowNodes(j.LoopConfig.Flow)
		}
		if len(j.LoopConfig.Rounds) > 0 {
			lcCopy.Rounds = make([]LoopRound, len(j.LoopConfig.Rounds))
			copy(lcCopy.Rounds, j.LoopConfig.Rounds)
		}
		if len(j.LoopConfig.Variables) > 0 {
			lcCopy.Variables = make(map[string]string, len(j.LoopConfig.Variables))
			for k, v := range j.LoopConfig.Variables {
				lcCopy.Variables[k] = v
			}
		}
		if len(j.LoopConfig.DisabledVars) > 0 {
			lcCopy.DisabledVars = make([]string, len(j.LoopConfig.DisabledVars))
			copy(lcCopy.DisabledVars, j.LoopConfig.DisabledVars)
		}
		cp.LoopConfig = &lcCopy
	}

	// Progress
	if j.Progress != nil {
		pCopy := *j.Progress
		if len(j.Progress.Results) > 0 {
			pCopy.Results = make([]IterationResult, len(j.Progress.Results))
			copy(pCopy.Results, j.Progress.Results)
			for i := range pCopy.Results {
				pCopy.Results[i].Path = CopyPath(j.Progress.Results[i].Path)
			}
		}
		if len(j.Progress.PersistWarnings) > 0 {
			pCopy.PersistWarnings = make([]string, len(j.Progress.PersistWarnings))
			copy(pCopy.PersistWarnings, j.Progress.PersistWarnings)
		}
		pCopy.CurrentPath = CopyPath(j.Progress.CurrentPath)
		pCopy.CurrentStartedAt = j.Progress.CurrentStartedAt
		if len(j.Progress.GroupActualIterations) > 0 {
			pCopy.GroupActualIterations = make(map[string]int, len(j.Progress.GroupActualIterations))
			for k, v := range j.Progress.GroupActualIterations {
				pCopy.GroupActualIterations[k] = v
			}
		}
		if len(j.Progress.GroupActualLeafCounts) > 0 {
			pCopy.GroupActualLeafCounts = make(map[string]int, len(j.Progress.GroupActualLeafCounts))
			for k, v := range j.Progress.GroupActualLeafCounts {
				pCopy.GroupActualLeafCounts[k] = v
			}
		}
		if len(j.Progress.SkippedPaths) > 0 {
			pCopy.SkippedPaths = make(map[string]bool, len(j.Progress.SkippedPaths))
			for k, v := range j.Progress.SkippedPaths {
				pCopy.SkippedPaths[k] = v
			}
		}
		cp.Progress = &pCopy
	}

	// Resume
	if j.Resume != nil {
		rCopy := *j.Resume
		rCopy.NextPath = CopyPath(j.Resume.NextPath)
		cp.Resume = &rCopy
	}

	return &cp
}

// DeepCopyFlowNodes recursively deep-copies a FlowNode slice.
func DeepCopyFlowNodes(nodes []FlowNode) []FlowNode {
	if nodes == nil {
		return nil
	}
	cp := make([]FlowNode, len(nodes))
	for i, n := range nodes {
		cp[i] = n
		if len(n.Children) > 0 {
			cp[i].Children = DeepCopyFlowNodes(n.Children)
		}
	}
	return cp
}

func newJobID() string {
	t := time.Now()
	var buf [4]byte
	rand.Read(buf[:])
	return fmt.Sprintf("job-%s-%06d-%s", t.Format("20060102-150405"), t.Nanosecond()/1000, hex.EncodeToString(buf[:]))
}

// BackfillFlowDefaults fills empty agent/model fields on step FlowNodes with
// the provided defaults. This is used when task-level or request-level
// defaults should be inherited by steps that don't specify their own.
func BackfillFlowDefaults(nodes []FlowNode, agentType string, modelID, acpMode, acpThoughtLevel string) {
	for i := range nodes {
		switch nodes[i].Type {
		case FlowNodeTypeStep:
			if nodes[i].AgentType == "" {
				nodes[i].AgentType = agentType
			}
			if nodes[i].StepModelID == "" {
				nodes[i].StepModelID = modelID
			}
			if nodes[i].ACPMode == "" {
				nodes[i].ACPMode = acpMode
			}
			if nodes[i].ACPThoughtLevel == "" {
				nodes[i].ACPThoughtLevel = acpThoughtLevel
			}
		case FlowNodeTypeGroup:
			BackfillFlowDefaults(nodes[i].Children, agentType, modelID, acpMode, acpThoughtLevel)
		}
	}
}
