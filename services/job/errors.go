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
	// ErrLoopConfigInvalid wraps user-submitted LoopConfig validation errors so
	// HTTP handlers can distinguish bad input (400) from service faults (500).
	ErrLoopConfigInvalid = errors.New("loopConfig is invalid")
	// ErrGracefulStopUnsupported is returned when a caller asks for graceful stop
	// while the active run is not a loop run. Interactive SendMessage runs do not
	// consume the graceful-stop flag, so reporting success would be misleading.
	ErrGracefulStopUnsupported = errors.New("graceful stop is only supported for loop runs")
	// ErrLoopStructureChanged is returned by UpdateRunningStepFields when the
	// submitted flow differs structurally from the running job's flow. Running
	// jobs may only edit per-step fields (prompt/model/agent/mode), not the
	// tree structure — the caller must stop the job first to restructure it.
	ErrLoopStructureChanged = errors.New("loop structure cannot be changed while running; stop the job first to edit structure")
)
