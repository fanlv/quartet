package model

type JobStartedEvent struct {
	BaseEvent
}

// RunOutcome describes the actual result of the run that just ended (an
// interactive send). Distinct from the job's overall status because an
// interactive send on an already-terminal job restores the prior status on
// the Job record — but the run itself may have completed/stopped/failed
// independently. Consumers that want to finalise in-flight UI state (tool
// bubbles, streaming messages) should use RunOutcome; consumers that display
// job-level status should use the event type as before.
type RunOutcome string

const (
	RunOutcomeCompleted RunOutcome = "completed"
	RunOutcomeStopped   RunOutcome = "stopped"
	RunOutcomeFailed    RunOutcome = "failed"
)

type JobCompletedEvent struct {
	BaseEvent
	RunOutcome          RunOutcome `json:"runOutcome,omitempty"`
	TotalTurnDurationMs int64      `json:"totalTurnDurationMs"`
}

type JobStoppedEvent struct {
	BaseEvent
	RunOutcome          RunOutcome `json:"runOutcome,omitempty"`
	TotalTurnDurationMs int64      `json:"totalTurnDurationMs"`
}

type JobFailedEvent struct {
	BaseEvent
	Message             string     `json:"message"`
	RunOutcome          RunOutcome `json:"runOutcome,omitempty"`
	TotalTurnDurationMs int64      `json:"totalTurnDurationMs"`
}
