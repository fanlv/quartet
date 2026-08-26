package job

import (
	"context"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// JobRunner encapsulates the logic to create sessions and run Agent turns.
// It is implemented by the handler layer which has access to all services.
// Used for interactive messages; Graph agent nodes use the same runner adapter.
type JobRunner interface {
	// InitSession creates a new session for the given job, returns session ID.
	// overrides may be nil, in which case job-level defaults are used.
	InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (sessionID string, err error)

	// RunIteration executes one Agent turn on the given session with the messages.
	// Agent events are delivered via the handler callback.
	RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error

	// SessionModelID returns the model id currently bound to sessionID. Used by
	// usage-stats to attribute a turn to its real-time session model. Returns "" when the session
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

// AcceptedMessagePreparer performs handler-owned metadata updates in the run
// goroutine, only after SendMessage has durably won the idempotency claim and
// before the Agent is invoked.
type AcceptedMessagePreparer interface {
	PrepareAcceptedMessage(ctx context.Context, jobID string) error
}
