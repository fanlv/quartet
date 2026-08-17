package job

import (
	"context"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// JobRunner encapsulates the logic to create sessions and run agent iterations.
// It is implemented by the handler layer which has access to all services.
// Used for both interactive (single message) and loop (preconfigured) modes.
type JobRunner interface {
	// InitSession creates a new session for the given job, returns session ID.
	// overrides may be nil, in which case job-level defaults are used.
	InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (sessionID string, err error)

	// RunIteration executes one agent run on the given session with the messages.
	// Agent events are delivered via the handler callback.
	RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error

	// SessionModelID returns the model id currently bound to sessionID. Used by
	// usage-stats to attribute a step to its real-time session model when the
	// FlowNode does not carry a per-step override. Returns "" when the session
	// is unknown or has no model — the caller treats empty as "skip model
	// bucket" rather than fabricating an attribution.
	SessionModelID(sessionID string) string
}

// PreparedExecutionReleaser is implemented by runners that acquire an external
// execution-admission lease before SendMessage changes Job state. The job
// service releases it on every asynchronous exit path; handlers release it when
// SendMessage rejects before the goroutine starts.
type PreparedExecutionReleaser interface {
	ReleasePreparedExecution()
}
