// Package sandbox is the runtime abstraction used by agent middlewares and
// any other callsite that needs more than plain file I/O (bash exec, web
// fetch, grep, browser, MCP host). A Client is always bound to a specific
// workspace — in Local form it points at a host directory, in Container
// form it points at the HTTP port published by a per-workspace sandbox
// container that Manager provisions on demand.
//
// The Manager is the only owner of container state. Call sites never talk
// to docker directly; they call New() and hand back the returned Client
// via Close() when they are done. Manager re-uses the container for the
// workspace's concurrent jobs and reclaims it once the workspace goes idle.
package sandbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	sdkSandbox "github.com/deep-agent/sandbox/sdk/go"
	"github.com/deep-agent/sandbox/types/model"
	"github.com/fanlv/quartet/pkg/fileserver"
)

// Sandbox is the full sandbox capability surface (bash + file + grep +
// jsonl + web + context). Alias of the upstream SDK interface so callers
// can pass a concrete *Client through as a Sandbox. File-only callers
// should depend on pkg/fileserver instead.
type Sandbox = sdkSandbox.Sandbox

// Client wraps a workspace-bound Sandbox implementation together with the
// context captured from the backend at connection time. The underlying
// implementation is either a local in-process client (Local form) or an
// HTTP client pointing at a Manager-owned container (Container form).
type Client struct {
	Client  sdkSandbox.Sandbox
	Ctx     *model.SandboxContext
	Workdir string

	// release is the Manager-supplied hook that returns this client's
	// lease. For Local form it is a no-op. Close() guards against double
	// release via releaseOnce so concurrent Close() callers can't both
	// decrement Manager's ref count.
	release     func()
	releaseOnce sync.Once
}

// Option configures a new sandbox Client. The set of options is kept tiny
// on purpose: runtime form is chosen by RunInSandbox, everything else is
// Manager's responsibility.
type Option func(*newConfig)

type newConfig struct {
	RunInSandbox bool
	RequestTO    time.Duration
	// Ctx is the caller-supplied context used to cancel a cold-start
	// (compose up + health probe). When nil, Manager falls back to
	// context.Background() with its internal timeouts. Callers that
	// already have a request-scoped ctx should pass it via WithContext
	// so an aborted HTTP request doesn't leave a 5-minute compose-up
	// hanging behind.
	Ctx context.Context
}

// WithRunInSandbox selects Container form when true. When false, or when
// the option is omitted, the Client is materialised against the local
// filesystem. The value is forwarded to Manager which ultimately decides
// whether to reuse an existing container or spin up a fresh one.
func WithRunInSandbox(runInSandbox bool) Option {
	return func(c *newConfig) {
		c.RunInSandbox = runInSandbox
	}
}

// WithContext lets the caller cancel a cold-start bring-up (compose up +
// health probe) via its own context. The cancellation only applies to the
// provisioning path; once the returned Client is handed back, container
// lifecycle is managed by the idle reaper as usual.
func WithContext(ctx context.Context) Option {
	return func(c *newConfig) {
		c.Ctx = ctx
	}
}

// New asks the Manager for a ready-to-use Sandbox bound to the given
// workspace. The workspace ID is the primary key: Manager uses it to
// re-use an already-running container for this workspace, to persist the
// SandboxRef back to the workspace metadata, and to key its internal
// maps. workdir is the host-side directory that should be visible inside
// the sandbox (bind mount source in Container form, shell cwd in Local
// form).
func New(workspaceID, workdir string, opts ...Option) (*Client, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("sandbox.New: workspace id is required")
	}
	cfg := &newConfig{RequestTO: 60 * time.Second}
	for _, opt := range opts {
		opt(cfg)
	}
	// Short-circuit for Local form: the Manager owns container lifecycle
	// and its lazy initialisation triggers `docker compose` recovery,
	// reaper goroutine, etc. None of that is needed for a plain in-process
	// client, and skipping it keeps test and non-sandboxed deployments
	// free of a docker dependency.
	if !cfg.RunInSandbox {
		return newLocalClient(workdir)
	}
	return getManager().acquire(workspaceID, workdir, cfg)
}

// Close releases the lease held by this Client. For Container form that
// decrements the Manager's reference count so the container can become
// eligible for idle reaping; for Local form it is a no-op. Safe to call
// multiple times and from multiple goroutines concurrently — the
// underlying release hook is guaranteed to run at most once.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.releaseOnce.Do(func() {
		if c.release != nil {
			c.release()
		}
	})
}

// GetFileManager returns the process-wide local file-only sandbox client.
// It exists for historical call sites that only need file I/O helpers.
// Prefer pkg/fileserver directly for new code.
func GetFileManager() fileserver.FileManager {
	return fileserver.GetFileManager()
}
