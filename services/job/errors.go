package job

import "errors"

// Sentinel errors returned by the job service. Handler layers can use
// errors.Is to map these to appropriate HTTP status codes.
var (
	ErrJobNotFound    = errors.New("job not found")
	ErrJobDeleted     = errors.New("job is deleted")
	ErrJobRunning     = errors.New("job is already running")
	ErrJobNotRunning  = errors.New("job is not running")
	ErrJobNotRunnable = errors.New("job is not in a runnable state")
	ErrNoLoopConfig   = errors.New("loopConfig is required")
	ErrNoResumable    = errors.New("job has no resumable progress")
	ErrEmptyMessage   = errors.New("SendMessage requires at least one message")
	// ErrLoopStructureChanged is returned by UpdateRunningStepFields when the
	// submitted flow differs structurally from the running job's flow. Running
	// jobs may only edit per-step fields (prompt/model/agent/mode), not the
	// tree structure — the caller must stop the job first to restructure it.
	ErrLoopStructureChanged = errors.New("loop structure cannot be changed while running; stop the job first to edit structure")
)
