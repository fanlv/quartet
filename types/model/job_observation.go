package model

// JobObservationResponse is a lightweight, global Job projection for clients
// that watch lifecycle transitions. ActiveJobs is a complete current snapshot
// across workspaces; Changes is a stable, cursor-ordered page of mutations.
// Reset tells a client to establish a fresh baseline instead of replaying the
// returned state as historical notifications.
type JobObservationResponse struct {
	ActiveJobs []JobSummary          `json:"activeJobs"`
	Changes    []JobObservationEvent `json:"changes"`
	Cursor     string                `json:"cursor"`
	HasMore    bool                  `json:"hasMore"`
	Reset      bool                  `json:"reset"`
}

// JobObservationEvent identifies one lifecycle change independently of the
// Job's current display ordering. EventID is stable across page retries and is
// suitable for client-side notification de-duplication.
type JobObservationEvent struct {
	EventID            string     `json:"eventId"`
	Job                JobSummary `json:"job"`
	PreviousState      JobStatus  `json:"previousStatus,omitempty"`
	GraphRunID         string     `json:"graphRunId,omitempty"`
	GraphStatus        string     `json:"graphStatus,omitempty"`
	PreviousGraphState string     `json:"previousGraphStatus,omitempty"`
	GraphSessionID     string     `json:"graphSessionId,omitempty"`
	RunOutcome         RunOutcome `json:"runOutcome,omitempty"`
	OccurredAt         int64      `json:"occurredAt"`
}
