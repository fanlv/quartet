package job

import "errors"

// Sentinel errors returned by the job service. Handler layers can use
// errors.Is to map these to appropriate HTTP status codes.
var (
	ErrJobNotFound             = errors.New("job not found")
	ErrJobDeleted              = errors.New("job is deleted")
	ErrJobRunning              = errors.New("job is already running")
	ErrJobNotRunning           = errors.New("job is not running")
	ErrJobNotRunnable          = errors.New("job is not in a runnable state")
	ErrEmptyMessage            = errors.New("SendMessage requires at least one message")
	ErrClientMessageIDConflict = errors.New("clientMessageId was already used for a different message")
)
