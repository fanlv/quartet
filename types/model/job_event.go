package model

type JobStartedEvent struct {
	BaseEvent
	TotalSteps int `json:"totalSteps"`
}

// RunOutcome describes the actual result of the run that just ended
// (the loop, or the interactive send). Distinct from the job's overall
// status because an interactive send on an already-terminal job restores
// the prior status on the Job record — but the run itself may have
// completed/stopped/failed independently. Consumers that want to finalise
// in-flight UI state (tool bubbles, streaming messages) should use
// RunOutcome; consumers that display job-level status should use the
// event type as before.
type RunOutcome string

const (
	RunOutcomeCompleted RunOutcome = "completed"
	RunOutcomeStopped   RunOutcome = "stopped"
	RunOutcomeFailed    RunOutcome = "failed"
)

type JobCompletedEvent struct {
	BaseEvent
	Progress   *JobProgress `json:"progress"`
	RunOutcome RunOutcome   `json:"runOutcome,omitempty"`
}

type JobStoppedEvent struct {
	BaseEvent
	Progress   *JobProgress `json:"progress"`
	RunOutcome RunOutcome   `json:"runOutcome,omitempty"`
}

type JobFailedEvent struct {
	BaseEvent
	Message    string       `json:"message"`
	Progress   *JobProgress `json:"progress"`
	RunOutcome RunOutcome   `json:"runOutcome,omitempty"`
}

type IterationStartedEvent struct {
	BaseEvent
	Message         string `json:"message,omitempty"`
	ClientMessageID string `json:"clientMessageId,omitempty"`
	ModelID         string `json:"modelId,omitempty"`
	AgentType       string `json:"agentType,omitempty"`
	ACPMode         string `json:"acpMode,omitempty"`
	ACPThoughtLevel string `json:"acpThoughtLevel,omitempty"`
}

type IterationCompletedEvent struct {
	BaseEvent
	Result *IterationResult `json:"result"`
}

type IterationFailedEvent struct {
	BaseEvent
	Result *IterationResult `json:"result"`
}
