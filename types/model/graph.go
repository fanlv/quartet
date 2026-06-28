package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// GraphWorkflowType distinguishes the two workflow libraries: workflows a user
// hand-authored in the Web UI ("user") versus workflows created/managed by a
// model through the CLI ("agent"). An empty type on disk (legacy data) is
// normalized to "user" at read time.
type GraphWorkflowType string

const (
	GraphWorkflowTypeUser  GraphWorkflowType = "user"
	GraphWorkflowTypeAgent GraphWorkflowType = "agent"
)

type GraphNodeType string

const (
	GraphNodeTypeStart   GraphNodeType = "start"
	GraphNodeTypeEnd     GraphNodeType = "end"
	GraphNodeTypeShell   GraphNodeType = "shell"
	GraphNodeTypePrompt  GraphNodeType = "prompt"
	GraphNodeTypeClarify GraphNodeType = "clarify"
	GraphNodeTypeIfElse  GraphNodeType = "ifElse"
	GraphNodeTypeLoop    GraphNodeType = "loop"
)

type GraphEdgePort string

const (
	GraphEdgePortDefault GraphEdgePort = "default"
	GraphEdgePortYes     GraphEdgePort = "yes"
	GraphEdgePortNo      GraphEdgePort = "no"
)

type GraphLoopMode string

const (
	GraphLoopModeFixed GraphLoopMode = "fixed"
	GraphLoopModeUntil GraphLoopMode = "until"
)

type GraphSessionStrategy string

const (
	GraphSessionStrategyNew     GraphSessionStrategy = "new"
	GraphSessionStrategyInherit GraphSessionStrategy = "inherit"
)

// GraphEndHookMode selects an End node's hook behavior. Empty is treated as
// GraphEndHookModeDefault.
type GraphEndHookMode string

const (
	GraphEndHookModeDefault GraphEndHookMode = "default" // run the global settings script
	GraphEndHookModeCustom  GraphEndHookMode = "custom"  // run the node's own HookScript
	GraphEndHookModeOff     GraphEndHookMode = "off"     // disable the hook
)

type GraphWorkflow struct {
	ID          string            `json:"id"`
	WorkspaceID string            `json:"workspaceId,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Type        GraphWorkflowType `json:"type,omitempty"`
	Config      GraphConfig       `json:"config"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	Deleted     bool              `json:"deleted,omitempty"`
}

type GraphConfig struct {
	Nodes        []GraphNode       `json:"nodes"`
	Edges        []GraphEdge       `json:"edges"`
	Variables    map[string]string `json:"variables,omitempty"`
	DisabledVars []string          `json:"disabledVars,omitempty"`
	Canvas       GraphCanvasState  `json:"canvas,omitempty"`
	RunConfig    GraphRunConfig    `json:"runConfig,omitempty"`
	WorkspaceID  string            `json:"workspaceId,omitempty"`
	Workdir      string            `json:"workdir,omitempty"`
	SandboxID    string            `json:"sandboxId,omitempty"`
}

type GraphNode struct {
	ID       string            `json:"id"`
	Type     GraphNodeType     `json:"type"`
	Title    string            `json:"title,omitempty"`
	ParentID string            `json:"parentId,omitempty"`
	Config   GraphNodeConfig   `json:"config,omitempty"`
	Layout   GraphNodeLayout   `json:"layout,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type GraphNodeConfig struct {
	Script             string               `json:"script,omitempty"`
	Prompt             string               `json:"prompt,omitempty"`
	AgentType          string               `json:"agentType,omitempty"`
	ModelID            string               `json:"modelId,omitempty"`
	ACPMode            string               `json:"acpMode,omitempty"`
	ACPThoughtLevel    string               `json:"acpThoughtLevel,omitempty"`
	SessionStrategy    GraphSessionStrategy `json:"sessionStrategy,omitempty"`
	OutputVariables    []string             `json:"outputVariables,omitempty"`
	LastAssistantAlias string               `json:"lastAssistantAlias,omitempty"`
	TimeoutSeconds     *int                 `json:"timeoutSeconds,omitempty"`
	Condition          string               `json:"condition,omitempty"`
	LoopMode           GraphLoopMode        `json:"loopMode,omitempty"`
	FixedCount         int                  `json:"fixedCount,omitempty"`
	UntilCondition     string               `json:"untilCondition,omitempty"`
	MaxIterations      int                  `json:"maxIterations,omitempty"`
	// HookScript is a side-effect shell script run AFTER a node completes
	// (notifications / logging / marking). Used by Prompt nodes (run when
	// non-empty) and by End nodes whose EndHookMode is "custom". It never
	// produces variables, never changes node status, and never fails the run —
	// a non-zero exit / timeout is logged and ignored.
	HookScript string `json:"hookScript,omitempty"`
	// EndHookMode selects an End node's hook behavior: "default" runs the global
	// settings script (GraphEndHookScript), "custom" runs this node's HookScript,
	// "off" disables it. Empty is treated as "default". End nodes only.
	EndHookMode GraphEndHookMode `json:"endHookMode,omitempty"`
}

type GraphNodeLayout struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width,omitempty"`
	Height float64 `json:"height,omitempty"`
}

type GraphEdge struct {
	ID           string            `json:"id"`
	SourceNodeID string            `json:"sourceNodeId"`
	TargetNodeID string            `json:"targetNodeId"`
	SourcePort   GraphEdgePort     `json:"sourcePort,omitempty"`
	TargetPort   string            `json:"targetPort,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type GraphCanvasState struct {
	Viewport GraphCanvasViewport `json:"viewport,omitempty"`
}

type GraphCanvasViewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}

type GraphRunConfig struct {
	ConcurrencyLimit      int   `json:"concurrencyLimit,omitempty"`
	DefaultNodeTimeoutSec int   `json:"defaultNodeTimeoutSec,omitempty"`
	JobTimeoutSec         int   `json:"jobTimeoutSec,omitempty"`
	DefaultLoopMaxIters   int   `json:"defaultLoopMaxIters,omitempty"`
	InstanceLimit         int   `json:"instanceLimit,omitempty"`
	SnapshotByteLimit     int64 `json:"snapshotByteLimit,omitempty"`
}

type GraphRunStatus string

const (
	GraphRunStatusPending      GraphRunStatus = "pending"
	GraphRunStatusRunning      GraphRunStatus = "running"
	GraphRunStatusCompleted    GraphRunStatus = "completed"
	GraphRunStatusFailed       GraphRunStatus = "failed"
	GraphRunStatusStepStopping GraphRunStatus = "stepStopping"
	GraphRunStatusStepStopped  GraphRunStatus = "stepStopped"
	GraphRunStatusStopped      GraphRunStatus = "stopped"
	GraphRunStatusTimedOut     GraphRunStatus = "timedOut"
	GraphRunStatusRecovering   GraphRunStatus = "recovering"
	// GraphRunStatusAwaitingInput is a resumable terminal: a clarify node ran its
	// turn and the run settled here waiting for the user to discuss in its session
	// and then continue. The scheduler has exited (like stopped); the bound
	// Job is non-running so the Chat append-message path accepts new turns. A
	// continue (ContinueRun) finalizes the awaiting clarify instances and resumes.
	GraphRunStatusAwaitingInput GraphRunStatus = "awaitingInput"
)

type GraphRun struct {
	ID             string             `json:"id"`
	WorkflowID     string             `json:"workflowId,omitempty"`
	JobID          string             `json:"jobId"`
	WorkspaceID    string             `json:"workspaceId,omitempty"`
	Status         GraphRunStatus     `json:"status"`
	BaseSnapshot   GraphRunSnapshot   `json:"baseSnapshot"`
	Versions       []GraphRunVersion  `json:"versions,omitempty"`
	Progress       *GraphProgress     `json:"progress,omitempty"`
	Resume         *GraphResumeState  `json:"resume,omitempty"`
	CurrentVersion int                `json:"currentVersion"`
	StartedAt      int64              `json:"startedAt,omitempty"`
	FinishedAt     int64              `json:"finishedAt,omitempty"`
	CreatedAt      time.Time          `json:"createdAt"`
	UpdatedAt      time.Time          `json:"updatedAt"`
	LastError      *GraphRuntimeError `json:"lastError,omitempty"`
	// ArchivedInstances preserves instances that a resume reset removed from the
	// live instance set but that carried a session (their conversation still
	// exists on disk). Resume drops failed/interrupted instances — and wholesale
	// resets every instance inside a touched loop, including already-succeeded
	// siblings — so without this the Chat session sidebar (which derives its list
	// from live instances) would lose those prior-attempt conversations the
	// instant a run is resumed. Keyed by instance-key string; accumulates across
	// resumes. Live instances always take precedence when a key reappears.
	ArchivedInstances map[string]GraphInstanceState `json:"archivedInstances,omitempty"`
}

type GraphRunSnapshot struct {
	WorkflowID     string                        `json:"workflowId,omitempty"`
	WorkflowName   string                        `json:"workflowName,omitempty"`
	Config         GraphConfig                   `json:"config"`
	ModelSnapshots map[string]ModelInstance      `json:"modelSnapshots,omitempty"`
	AgentSnapshots map[string]GraphAgentSnapshot `json:"agentSnapshots,omitempty"`
	CapturedAt     int64                         `json:"capturedAt"`
}

type GraphAgentSnapshot struct {
	AgentType       string `json:"agentType"`
	ModelID         string `json:"modelId,omitempty"`
	ACPMode         string `json:"acpMode,omitempty"`
	ACPThoughtLevel string `json:"acpThoughtLevel,omitempty"`
	SystemPrompt    string `json:"systemPrompt,omitempty"`
}

type GraphRunVersion struct {
	Version        int                           `json:"version"`
	Config         GraphConfig                   `json:"config"`
	ModelSnapshots map[string]ModelInstance      `json:"modelSnapshots,omitempty"`
	AgentSnapshots map[string]GraphAgentSnapshot `json:"agentSnapshots,omitempty"`
	Reason         string                        `json:"reason,omitempty"`
	CreatedAt      int64                         `json:"createdAt"`
	CreatedBy      string                        `json:"createdBy,omitempty"`
}

type GraphInstanceStatus string

const (
	GraphInstanceStatusPending     GraphInstanceStatus = "pending"
	GraphInstanceStatusRunning     GraphInstanceStatus = "running"
	GraphInstanceStatusSucceeded   GraphInstanceStatus = "succeeded"
	GraphInstanceStatusFailed      GraphInstanceStatus = "failed"
	GraphInstanceStatusSkipped     GraphInstanceStatus = "skipped"
	GraphInstanceStatusInterrupted GraphInstanceStatus = "interrupted"
	// GraphInstanceStatusAwaitingInput marks a clarify instance that ran its turn
	// and is holding its out-edges, waiting for the user to discuss in its session
	// and continue. It is a non-terminal hold (not resettable on resume): a
	// continue finalizes it (capture结论 → succeeded → resolve out-edges).
	GraphInstanceStatusAwaitingInput GraphInstanceStatus = "awaitingInput"
)

type GraphInstanceKey struct {
	NodeID     string               `json:"nodeId"`
	Iterations []GraphLoopIteration `json:"iterations,omitempty"`
}

type GraphLoopIteration struct {
	LoopNodeID string `json:"loopNodeId"`
	Index      int    `json:"index"`
}

type GraphInstanceState struct {
	Key       GraphInstanceKey    `json:"key"`
	NodeID    string              `json:"nodeId"`
	NodeTitle string              `json:"nodeTitle,omitempty"`
	NodeType  GraphNodeType       `json:"nodeType"`
	Status    GraphInstanceStatus `json:"status"`
	Version   int                 `json:"version"`
	BatchID   string              `json:"batchId,omitempty"`
	// SessionID is the instance's outflow session for §3 会话血缘: the session a
	// downstream `inherit` Agent forks from. Agent nodes set it to their own
	// created/forked session; non-Agent nodes pass the inflow session through.
	SessionID string `json:"sessionId,omitempty"`
	// DisplaySessionID is the session whose conversation the UI lists/opens for
	// this instance. Agent nodes leave it empty (their SessionID is the display
	// session). Shell nodes set it to their own recording session — the script +
	// output transcript — which is intentionally kept out of SessionID so it
	// never becomes a lineage parent for downstream `inherit` Agents.
	DisplaySessionID string             `json:"displaySessionId,omitempty"`
	VisibleVariables map[string]string  `json:"visibleVariables,omitempty"`
	OutputVariables  map[string]string  `json:"outputVariables,omitempty"`
	VariableWriters  map[string]string  `json:"variableWriters,omitempty"`
	StartedAt        int64              `json:"startedAt,omitempty"`
	FinishedAt       int64              `json:"finishedAt,omitempty"`
	DurationMs       int64              `json:"durationMs,omitempty"`
	Error            *GraphRuntimeError `json:"error,omitempty"`
	BlockedReason    string             `json:"blockedReason,omitempty"`
}

type GraphEdgeStatus string

const (
	GraphEdgeStatusPending GraphEdgeStatus = "pending"
	GraphEdgeStatusActive  GraphEdgeStatus = "active"
	GraphEdgeStatusPruned  GraphEdgeStatus = "pruned"
)

type GraphEdgeState struct {
	EdgeID            string           `json:"edgeId"`
	SourceInstanceKey GraphInstanceKey `json:"sourceInstanceKey"`
	TargetInstanceKey GraphInstanceKey `json:"targetInstanceKey"`
	Status            GraphEdgeStatus  `json:"status"`
	ResolvedAt        int64            `json:"resolvedAt,omitempty"`
	Reason            string           `json:"reason,omitempty"`
}

type GraphProgress struct {
	TotalCount       int                           `json:"totalCount"`
	CompletedCount   int                           `json:"completedCount"`
	FailedCount      int                           `json:"failedCount"`
	SkippedCount     int                           `json:"skippedCount"`
	InterruptedCount int                           `json:"interruptedCount"`
	RunningCount     int                           `json:"runningCount"`
	CurrentKeys      []GraphInstanceKey            `json:"currentKeys,omitempty"`
	Instances        map[string]GraphInstanceState `json:"instances,omitempty"`
	LastError        string                        `json:"lastError,omitempty"`
}

type GraphResumeState struct {
	ReadyKeys           []GraphInstanceKey             `json:"readyKeys,omitempty"`
	EdgeStates          map[string]GraphEdgeState      `json:"edgeStates,omitempty"`
	LoopState           map[string]GraphLoopState      `json:"loopState,omitempty"`
	VariablesByKey      map[string]map[string]string   `json:"variablesByKey,omitempty"`
	SessionLineageByKey map[string]GraphSessionLineage `json:"sessionLineageByKey,omitempty"`
	FrozenBatch         *GraphReadyBatch               `json:"frozenBatch,omitempty"`
}

type GraphLoopState struct {
	LoopNodeID       string            `json:"loopNodeId"`
	InstanceKey      GraphInstanceKey  `json:"instanceKey"`
	CurrentIteration int               `json:"currentIteration"`
	Completed        bool              `json:"completed"`
	Variables        map[string]string `json:"variables,omitempty"`
	VariableWriters  map[string]string `json:"variableWriters,omitempty"`
	// EntrySession is the session flowing into the current round (the loop
	// scope's roundEntrySession). Persisted so resume can rebuild the in-flight
	// round's inflow session and a session-inheriting first body Agent forks the
	// correct upstream session rather than minting a new one.
	EntrySession string `json:"entrySession,omitempty"`
}

type GraphSessionLineage struct {
	Strategy          GraphSessionStrategy `json:"strategy"`
	SessionID         string               `json:"sessionId,omitempty"`
	ParentSessionID   string               `json:"parentSessionId,omitempty"`
	ParentInstanceKey *GraphInstanceKey    `json:"parentInstanceKey,omitempty"`
}

type GraphReadyBatch struct {
	ID        string                           `json:"id"`
	Version   int                              `json:"version"`
	Members   map[string]GraphReadyBatchMember `json:"members"`
	CreatedAt int64                            `json:"createdAt"`
}

type GraphReadyBatchMember struct {
	Key    GraphInstanceKey    `json:"key"`
	Status GraphInstanceStatus `json:"status"`
}

type GraphValidationErrorType string

const (
	GraphValidationErrorTypeStructure GraphValidationErrorType = "structure"
	GraphValidationErrorTypeNode      GraphValidationErrorType = "node"
	GraphValidationErrorTypeEdge      GraphValidationErrorType = "edge"
	GraphValidationErrorTypeVariable  GraphValidationErrorType = "variable"
	GraphValidationErrorTypeConfig    GraphValidationErrorType = "config"
	GraphValidationErrorTypeSession   GraphValidationErrorType = "session"
)

type GraphValidationError struct {
	Type      GraphValidationErrorType `json:"type"`
	Message   string                   `json:"message"`
	NodeID    string                   `json:"nodeId,omitempty"`
	EdgeID    string                   `json:"edgeId,omitempty"`
	Variable  string                   `json:"variable,omitempty"`
	ConfigKey string                   `json:"configKey,omitempty"`
}

type GraphRuntimeError struct {
	RunID       string            `json:"runId,omitempty"`
	InstanceKey *GraphInstanceKey `json:"instanceKey,omitempty"`
	NodeID      string            `json:"nodeId,omitempty"`
	NodeTitle   string            `json:"nodeTitle,omitempty"`
	NodeType    GraphNodeType     `json:"nodeType,omitempty"`
	Message     string            `json:"message"`
	RetryCount  int               `json:"retryCount,omitempty"`
	CanResume   bool              `json:"canResume"`
	Stdout      string            `json:"stdout,omitempty"`
	Stderr      string            `json:"stderr,omitempty"`
	ExitCode    *int              `json:"exitCode,omitempty"`
	ModelOutput string            `json:"modelOutput,omitempty"`
	Details     map[string]string `json:"details,omitempty"`
}

type GraphEventType string

const (
	GraphEventTypeInstanceStarted   GraphEventType = "instanceStarted"
	GraphEventTypeInstanceCompleted GraphEventType = "instanceCompleted"
	GraphEventTypeInstanceFailed    GraphEventType = "instanceFailed"
	GraphEventTypeInstanceSkipped   GraphEventType = "instanceSkipped"
	GraphEventTypeEdgeResolved      GraphEventType = "edgeResolved"
	GraphEventTypeVariableWritten   GraphEventType = "variableWritten"
	GraphEventTypeLoopIteration     GraphEventType = "loopIteration"
	GraphEventTypeProgressUpdated   GraphEventType = "progressUpdated"
	GraphEventTypeAgentMessageStart GraphEventType = "agentMessageStart"
	GraphEventTypeAgentMessageDelta GraphEventType = "agentMessageDelta"
	GraphEventTypeAgentMessageEnd   GraphEventType = "agentMessageEnd"
	GraphEventTypeAgentThoughtStart GraphEventType = "agentThoughtStart"
	GraphEventTypeAgentThoughtDelta GraphEventType = "agentThoughtDelta"
	GraphEventTypeAgentThoughtEnd   GraphEventType = "agentThoughtEnd"
	GraphEventTypeAgentToolStart    GraphEventType = "agentToolStart"
	GraphEventTypeAgentToolArgs     GraphEventType = "agentToolArgs"
	GraphEventTypeAgentToolResult   GraphEventType = "agentToolResult"
	GraphEventTypeAgentToolEnd      GraphEventType = "agentToolEnd"
	GraphEventTypeAgentTokenUsage   GraphEventType = "agentTokenUsage"
	GraphEventTypeLog               GraphEventType = "log"
	GraphEventTypeError             GraphEventType = "error"
	// GraphEventTypeHookCompleted / GraphEventTypeHookFailed (§ 节点 Hook) carry a
	// node hook's execution result (exit code, truncated stdout/stderr, origin) so
	// the run-view node-detail panel can surface what a side-effect script did. A
	// hook NEVER affects node status or the run; these are the ONLY signal a user
	// gets that a configured hook ran and how it ended. Both persist to events.jsonl.
	GraphEventTypeHookCompleted GraphEventType = "hookCompleted"
	GraphEventTypeHookFailed    GraphEventType = "hookFailed"
)

type GraphEvent struct {
	ID          string             `json:"id"`
	RunID       string             `json:"runId"`
	Type        GraphEventType     `json:"type"`
	InstanceKey *GraphInstanceKey  `json:"instanceKey,omitempty"`
	NodeID      string             `json:"nodeId,omitempty"`
	EdgeID      string             `json:"edgeId,omitempty"`
	Message     string             `json:"message,omitempty"`
	Payload     map[string]string  `json:"payload,omitempty"`
	Error       *GraphRuntimeError `json:"error,omitempty"`
	CreatedAt   int64              `json:"createdAt"`
}

type CreateGraphWorkflowRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Type        GraphWorkflowType `json:"type,omitempty"`
	WorkspaceID string            `json:"workspaceId,omitempty"`
	Config      GraphConfig       `json:"config"`
}

type UpdateGraphWorkflowRequest struct {
	Name        *string      `json:"name,omitempty"`
	Description *string      `json:"description,omitempty"`
	WorkspaceID *string      `json:"workspaceId,omitempty"`
	Config      *GraphConfig `json:"config,omitempty"`
	UpdatedAt   *time.Time   `json:"updatedAt,omitempty"`
}

type DeleteGraphWorkflowRequest struct {
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

type ValidateGraphWorkflowRequest struct {
	Config GraphConfig `json:"config"`
}

type StartGraphRunRequest struct {
	WorkflowID        string       `json:"workflowId,omitempty"`
	WorkflowUpdatedAt *time.Time   `json:"workflowUpdatedAt,omitempty"`
	JobID             string       `json:"jobId,omitempty"`
	WorkspaceID       string       `json:"workspaceId,omitempty"`
	Workdir           string       `json:"workdir,omitempty"`
	Config            *GraphConfig `json:"config,omitempty"`
}

type UpdateGraphRunVersionRequest struct {
	Config GraphConfig `json:"config"`
	Reason string      `json:"reason,omitempty"`
}

type GraphRunActionRequest struct {
	Reason string `json:"reason,omitempty"`
}

type GraphListWorkflowsResponse struct {
	Workflows []GraphWorkflowSummary `json:"workflows"`
	// Warnings surfaces workflow files that could not be read or parsed during a
	// list. They are skipped from Workflows (so one bad file does not break the
	// whole page) but reported here so the UI can show the offending file and the
	// raw error instead of letting the workflow silently vanish from the list.
	Warnings []GraphWorkflowWarning `json:"warnings,omitempty"`
}

type GraphWorkflowSummary struct {
	ID          string            `json:"id"`
	WorkspaceID string            `json:"workspaceId,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Type        GraphWorkflowType `json:"type,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	NodeCount   int               `json:"nodeCount"`
	EdgeCount   int               `json:"edgeCount"`
}

// GraphWorkflowWarning describes a workflow file that was skipped during a list
// because it was unreadable or malformed. File is the on-disk path; Error is the
// full underlying error text (per the repo convention of never hiding errors).
type GraphWorkflowWarning struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

type GraphWorkflowResponse struct {
	Workflow *GraphWorkflow         `json:"workflow,omitempty"`
	Errors   []GraphValidationError `json:"errors,omitempty"`
}

type GraphValidationResponse struct {
	Valid  bool                   `json:"valid"`
	Errors []GraphValidationError `json:"errors,omitempty"`
}

type GraphRunResponse struct {
	Run    *GraphRun              `json:"run,omitempty"`
	Errors []GraphValidationError `json:"errors,omitempty"`
}

type GraphRunStatusResponse struct {
	Run       *GraphRun            `json:"run,omitempty"`
	Progress  *GraphProgress       `json:"progress,omitempty"`
	Instances []GraphInstanceState `json:"instances,omitempty"`
	Edges     []GraphEdgeState     `json:"edges,omitempty"`
	// EventCount is the number of persisted event lines for this run. The
	// client uses it only as the initial SSE resume cursor seed; it never
	// needs the event bodies in the status response (the SSE stream and
	// per-session message history carry those). Lets GetRunStatus stop
	// serialising the whole event log into the status payload.
	EventCount int `json:"eventCount,omitempty"`
}

type GraphRunEventsResponse struct {
	Events    []GraphEvent `json:"events"`
	NextLine  int          `json:"nextLine"`
	LastEvent string       `json:"lastEventId,omitempty"`
}

// GraphHookResult is one node hook's execution result, derived from a
// hookCompleted/hookFailed event for the run-view node-detail panel. NodeTitle /
// NodeType are carried in the event payload (captured at fire time from the run's
// own config) so the panel needs no access to the editor node list. FinishedAt is
// the event's CreatedAt, used to keep the latest result per node when a resume
// rollback re-fires a hook.
type GraphHookResult struct {
	NodeID     string        `json:"nodeId"`
	NodeTitle  string        `json:"nodeTitle,omitempty"`
	NodeType   GraphNodeType `json:"nodeType,omitempty"`
	Source     string        `json:"source,omitempty"` // "prompt" | "end"
	Status     string        `json:"status"`           // "completed" | "failed"
	ExitCode   *int          `json:"exitCode,omitempty"`
	Stdout     string        `json:"stdout,omitempty"`
	Stderr     string        `json:"stderr,omitempty"`
	Message    string        `json:"message,omitempty"`
	FinishedAt int64         `json:"finishedAt"`
}

type GraphHookResultsResponse struct {
	Results []GraphHookResult `json:"results"`
}

func NewGraphWorkflowID() string {
	return newGraphID("gwf")
}

func NewGraphRunID() string {
	return newGraphID("grun")
}

func NewGraphEventID() string {
	return newGraphID("gevt")
}

func newGraphID(prefix string) string {
	t := time.Now()
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("%s-%s-%06d-%s", prefix, t.Format("20060102-150405"), t.Nanosecond()/1000, hex.EncodeToString(buf[:]))
}
