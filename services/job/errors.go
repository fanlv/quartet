package job

import "errors"

// Sentinel errors returned by the job service. Handler layers can use
// errors.Is to map these to appropriate HTTP status codes.
var (
	ErrJobNotFound    = errors.New("job not found")
	ErrJobDeleted     = errors.New("job is deleted")
	ErrJobRunning     = errors.New("job is already running")
	ErrJobNotRunnable = errors.New("job is not in a runnable state")
	ErrNoLoopConfig   = errors.New("loopConfig is required")
	ErrNoResumable    = errors.New("job has no resumable progress")
	ErrEmptyMessage   = errors.New("SendMessage requires at least one message")
)
