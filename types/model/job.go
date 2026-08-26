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
	JobModeGraph       JobMode = "graph"
)

// ClientMessageState records the durable disposition of one idempotent
// interactive message submission. A processing receipt is written before the
// Agent goroutine starts. Once written, the same clientMessageId is never
// executed again; callers that intentionally retry a failed/stopped message
// must submit it with a new ID.
type ClientMessageState string

const (
	ClientMessageStateQueued      ClientMessageState = "queued"
	ClientMessageStateBlocked     ClientMessageState = "blocked"
	ClientMessageStateProcessing  ClientMessageState = "processing"
	ClientMessageStateCompleted   ClientMessageState = "completed"
	ClientMessageStateFailed      ClientMessageState = "failed"
	ClientMessageStateStopped     ClientMessageState = "stopped"
	ClientMessageStateInterrupted ClientMessageState = "interrupted"
	ClientMessageStateDeleted     ClientMessageState = "deleted"
)

// ClientMessageReceipt is the persistent idempotency record for a
// clientMessageId. PayloadHash prevents one key from silently being reused for
// a different logical request.
type ClientMessageReceipt struct {
	State       ClientMessageState `json:"state"`
	PayloadHash string             `json:"payloadHash"`
	AcceptedAt  int64              `json:"acceptedAt"`
	FinishedAt  int64              `json:"finishedAt,omitempty"`
}

type QueuedMessageState string

const (
	QueuedMessageStateQueued     QueuedMessageState = "queued"
	QueuedMessageStateBlocked    QueuedMessageState = "blocked"
	QueuedMessageStateProcessing QueuedMessageState = "processing"
)

const (
	MessageQueuePauseUserStopped = "user_stopped"
	MessageQueuePauseBlocked     = "blocked"
)

// QueuedJobMessage is a durable, not-yet-started interactive message. It is
// hidden from ordinary Job JSON and exposed only through the queue API.
type QueuedJobMessage struct {
	ID              string             `json:"id"`
	Messages        []RequestMessage   `json:"messages"`
	SessionID       string             `json:"sessionId,omitempty"`
	AgentType       string             `json:"agentType,omitempty"`
	AgentID         string             `json:"agentId,omitempty"`
	AgentRevision   string             `json:"agentRevision,omitempty"`
	ModelID         string             `json:"modelId,omitempty"`
	ACPMode         string             `json:"acpMode,omitempty"`
	ACPThoughtLevel string             `json:"acpThoughtLevel,omitempty"`
	BypassCommand   bool               `json:"bypassCommand,omitempty"`
	Source          string             `json:"source,omitempty"`
	ActorID         string             `json:"actorId,omitempty"`
	State           QueuedMessageState `json:"state"`
	Error           string             `json:"error,omitempty"`
	CreatedAt       int64              `json:"createdAt"`
	UpdatedAt       int64              `json:"updatedAt"`
}

type MessageQueueSnapshot struct {
	JobID        string             `json:"jobId"`
	Version      int64              `json:"version"`
	Paused       bool               `json:"paused"`
	PauseReason  string             `json:"pauseReason,omitempty"`
	WillContinue bool               `json:"willContinue"`
	Active       *QueuedJobMessage  `json:"active,omitempty"`
	Items        []QueuedJobMessage `json:"items"`
}

// CommandReceipt records a completed slash-command dispatch. Commands use a
// separate receipt type because they execute synchronously and must never leave
// an Agent-style processing receipt behind. Event is persisted so retries can
// replay the original response without repeating side effects.
type CommandReceipt struct {
	PayloadHash string                     `json:"payloadHash"`
	Event       *CommandSystemMessageEvent `json:"event"`
}

// Job represents a single execution unit: an interactive chat or graph run.
//
// # Field ownership model
//
// The in-memory *Job pointer is shared between the handler goroutine and the
// run goroutine. To prevent data races, fields are split by ownership
// and ALL access MUST be protected by service.mu:
//
// Immutable after creation (safe to read without lock):
//
//	ID, WorkspaceID, CreatedAt, ScheduleID, TimeoutMinutes, InitialAgentID,
//	InitialACPMode, InitialACPThoughtLevel
//
// Handler-owned (written by handler through targeted service mutators):
//
//	Title, Mode, Workdir, ShareToken, Deleted, PinnedAt, UpdatedAt
//
// Run-owned (written by the run goroutine):
//
//	Status, StartedAt, FinishedAt, GraphRunID, Progress, SessionIDs
//
// Creation-initialized and service-maintained state:
//
//	FirstModelID, ActiveClientMessageID, ClientMessageReceipts, CommandReceipts,
//	CreationClientMessageID, CreationPayloadHash
type Job struct {
	// --- Immutable after creation ---
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`

	// --- Handler-owned ---
	Title                string    `json:"title"`
	UpdatedAt            time.Time `json:"updatedAt"`
	Deleted              bool      `json:"deleted,omitempty"`
	PinnedAt             int64     `json:"pinnedAt,omitempty"` // unix ms; 0 means not pinned
	Mode                 JobMode   `json:"mode"`
	Workdir              string    `json:"workdir,omitempty"`
	ShareToken           string    `json:"shareToken,omitempty"`
	TitleGenerationError string    `json:"titleGenerationError,omitempty"`

	// --- Immutable after creation ---
	WorkspaceID    string `json:"workspaceId"`
	ScheduleID     string `json:"scheduleId,omitempty"`
	TimeoutMinutes int    `json:"timeoutMinutes,omitempty"`
	// Initial* preserves the configuration chosen when an interactive Job is
	// created. A Job has no Session yet at that point, so session metadata cannot
	// recover these values for list rendering or the first send after a client
	// reload. Once a Session exists, its metadata remains authoritative.
	InitialAgentID         string `json:"initialAgentId,omitempty"`
	InitialACPMode         string `json:"initialAcpMode,omitempty"`
	InitialACPThoughtLevel string `json:"initialAcpThoughtLevel,omitempty"`

	// --- Run-owned ---
	Status     JobStatus `json:"status"`
	StartedAt  int64     `json:"startedAt,omitempty"`  // unix ms; set when execution begins
	FinishedAt int64     `json:"finishedAt,omitempty"` // unix ms; set when terminal state reached
	GraphRunID string    `json:"graphRunId,omitempty"`
	SessionIDs []string  `json:"sessionIds"`
	// GraphSessionIDs lists the sessions opened by Agent nodes of this job's
	// graph run, kept separate from SessionIDs so they never pollute the
	// linear chat/archive semantics of SessionIDs (e.g. the
	// "last entry is the active session" assumption in resolveSessionID, or
	// SessionCount = len(SessionIDs)). It exists purely as an authorization
	// whitelist: an interactive message may target any session in here so a
	// user can keep chatting in a finished graph node's session after the run
	// stops. Graph nodes run concurrently, so order here is non-linear and must
	// not be read as an iteration sequence.
	GraphSessionIDs []string     `json:"graphSessionIds,omitempty"`
	Progress        *JobProgress `json:"progress,omitempty"`

	// LastRunOutcome records the actual outcome of the most recent interactive
	// send. For interactive sends on an
	// already-terminal job, job.Status is the restored prior status while
	// LastRunOutcome reflects what actually happened in the send. Frontends
	// that recover from a missed terminal SSE event should use this field
	// instead of job.Status to finalize in-flight UI state.
	LastRunOutcome RunOutcome `json:"lastRunOutcome,omitempty"`

	// Idempotency metadata is persisted by repository.JobRepo but hidden from
	// ordinary/public Job JSON responses. Message receipts provide durable
	// at-most-once Agent execution; command receipts replay synchronous command
	// results; creation fields make downstream actions such as /new retry-safe.
	ActiveClientMessageID   string                          `json:"-"`
	ClientMessageReceipts   map[string]ClientMessageReceipt `json:"-"`
	CommandReceipts         map[string]CommandReceipt       `json:"-"`
	MessageQueue            []QueuedJobMessage              `json:"-"`
	MessageQueueVersion     int64                           `json:"-"`
	MessageQueuePaused      bool                            `json:"-"`
	MessageQueuePauseReason string                          `json:"-"`
	CreationClientMessageID string                          `json:"-"`
	CreationPayloadHash     string                          `json:"-"`

	// FirstModelID initially records the model selected for an empty interactive
	// Job, then represents the first non-deleted session's ModelID for listing.
	// Denormalized on the write path so JobList does not
	// need to open every session file (avoids an O(jobs * sessions) I/O hit
	// on the list endpoint). An empty value means "not cached yet" — the
	// lister may lazily fill it and persist.
	FirstModelID string `json:"firstModelId,omitempty"`
}

// SessionOverrides carries optional agent/model overrides for session creation.
// Zero/empty values mean "use job-level defaults".
type SessionOverrides struct {
	AgentType       string
	AgentBinding    *AgentRuntimeBinding
	ModelID         string
	ACPMode         string
	ACPThoughtLevel string
}

// JobProgress stores durable diagnostics for a Job run.
// When adding new slice/map/pointer fields, update Job.DeepCopy accordingly.
type JobProgress struct {
	// LastError is the most recent failure reason. Persisted so refreshes still
	// surface it.
	LastError string `json:"lastError,omitempty"`
	// PersistWarnings records best-effort persistence failures without clobbering
	// LastError. Persistence warnings describe disk/state divergence, while
	// LastError remains reserved for the user-visible run failure reason.
	PersistWarnings []string `json:"persistWarnings,omitempty"`
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

	// GraphSessionIDs
	if len(j.GraphSessionIDs) > 0 {
		cp.GraphSessionIDs = make([]string, len(j.GraphSessionIDs))
		copy(cp.GraphSessionIDs, j.GraphSessionIDs)
	}

	// Progress
	if j.Progress != nil {
		pCopy := *j.Progress
		if len(j.Progress.PersistWarnings) > 0 {
			pCopy.PersistWarnings = make([]string, len(j.Progress.PersistWarnings))
			copy(pCopy.PersistWarnings, j.Progress.PersistWarnings)
		}
		cp.Progress = &pCopy
	}

	// ClientMessageReceipts
	if len(j.ClientMessageReceipts) > 0 {
		cp.ClientMessageReceipts = make(map[string]ClientMessageReceipt, len(j.ClientMessageReceipts))
		for id, receipt := range j.ClientMessageReceipts {
			cp.ClientMessageReceipts[id] = receipt
		}
	}
	if len(j.CommandReceipts) > 0 {
		cp.CommandReceipts = make(map[string]CommandReceipt, len(j.CommandReceipts))
		for id, receipt := range j.CommandReceipts {
			copyReceipt := receipt
			if receipt.Event != nil {
				copyEvent := *receipt.Event
				if receipt.Event.External != nil {
					copyEvent.External = make(map[string]any, len(receipt.Event.External))
					for key, value := range receipt.Event.External {
						copyEvent.External[key] = value
					}
				}
				if receipt.Event.Action != nil {
					copyAction := *receipt.Event.Action
					copyEvent.Action = &copyAction
				}
				copyReceipt.Event = &copyEvent
			}
			cp.CommandReceipts[id] = copyReceipt
		}
	}
	if len(j.MessageQueue) > 0 {
		cp.MessageQueue = make([]QueuedJobMessage, len(j.MessageQueue))
		for i := range j.MessageQueue {
			cp.MessageQueue[i] = j.MessageQueue[i]
			if len(j.MessageQueue[i].Messages) > 0 {
				cp.MessageQueue[i].Messages = make([]RequestMessage, len(j.MessageQueue[i].Messages))
				copy(cp.MessageQueue[i].Messages, j.MessageQueue[i].Messages)
				for k := range cp.MessageQueue[i].Messages {
					cp.MessageQueue[i].Messages[k].ImageUrls = append([]string(nil), j.MessageQueue[i].Messages[k].ImageUrls...)
					cp.MessageQueue[i].Messages[k].FileAttachments = append([]FileAttachment(nil), j.MessageQueue[i].Messages[k].FileAttachments...)
				}
			}
		}
	}

	return &cp
}

func newJobID() string {
	t := time.Now()
	var buf [4]byte
	rand.Read(buf[:])
	return fmt.Sprintf("job-%s-%06d-%s", t.Format("20060102-150405"), t.Nanosecond()/1000, hex.EncodeToString(buf[:]))
}
