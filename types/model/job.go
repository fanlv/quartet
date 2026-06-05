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
//	Status, StartedAt, FinishedAt, LoopConfig, Progress, Resume, SessionIDs
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
	ScriptID    string    `json:"scriptId,omitempty"`
	ScriptName  string    `json:"scriptName,omitempty"`

	// Per-step agent/model overrides (only used when RoundMode != RoundModeNone)
	AgentType   string `json:"agentType,omitempty"`
	StepModelID string `json:"modelId,omitempty"`
	ACPMode     string `json:"acpMode,omitempty"`

	// ContinueOnError allows the workflow to continue even if this step fails.
	// When true, a failed step is recorded but does not fail the entire job.
	ContinueOnError bool `json:"continueOnError,omitempty"`

	// Group fields (Type == "group")
	IterationCount int        `json:"iterationCount,omitempty"`
	Children       []FlowNode `json:"children,omitempty"`
}

// SessionOverrides carries optional per-step agent/model overrides for session creation.
// Zero/empty values mean "use job-level defaults".
type SessionOverrides struct {
	AgentType string
	ModelID   string
	ACPMode   string
}

// LoopConfig defines the execution plan for a loop job.
// The canonical field is Flow (recursive tree). The legacy fields IterationCount
// and Rounds are kept for backward-compatible deserialization of old jobs/templates;
// call MigrateLoopConfig to normalize them into Flow before execution.
type LoopConfig struct {
	Flow      []FlowNode        `json:"flow,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`

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
	ScriptID    string    `json:"scriptId,omitempty"`
	ScriptName  string    `json:"scriptName,omitempty"`
}

type JobProgress struct {
	TotalSteps     int               `json:"totalSteps"`
	CurrentPath    []int             `json:"currentPath,omitempty"`
	CompletedCount int               `json:"completedCount"`
	FailedCount    int               `json:"failedCount"`
	Results        []IterationResult `json:"results,omitempty"`
	// LastError is the most recent failure reason — either from an iteration
	// failure (captured alongside IterationResult.Error) or a job-level
	// failure (panic / failJob). Persisted so refreshes still surface it.
	LastError string `json:"lastError,omitempty"`
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
			lcCopy.Flow = deepCopyFlowNodes(j.LoopConfig.Flow)
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
		pCopy.CurrentPath = CopyPath(j.Progress.CurrentPath)
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

// deepCopyFlowNodes recursively deep-copies a FlowNode slice.
func deepCopyFlowNodes(nodes []FlowNode) []FlowNode {
	if nodes == nil {
		return nil
	}
	cp := make([]FlowNode, len(nodes))
	for i, n := range nodes {
		cp[i] = n
		if len(n.Children) > 0 {
			cp[i].Children = deepCopyFlowNodes(n.Children)
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
func BackfillFlowDefaults(nodes []FlowNode, agentType string, modelID, acpMode string) {
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
		case FlowNodeTypeGroup:
			BackfillFlowDefaults(nodes[i].Children, agentType, modelID, acpMode)
		}
	}
}
