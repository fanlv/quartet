package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	sdkSandbox "github.com/deep-agent/sandbox/sdk/go"
	"github.com/deep-agent/sandbox/sdk/go/local"
	sbmodel "github.com/deep-agent/sandbox/types/model"
	"github.com/fanlv/quartet/einocli/chatctx"
	"github.com/fanlv/quartet/einocli/logger"
	"github.com/fanlv/quartet/einocli/middlewares"
	"github.com/fanlv/quartet/einocli/modelbuilder"
	"github.com/fanlv/quartet/einocli/round"
	"github.com/fanlv/quartet/einocli/store"
)

type AgentEvent = adk.AgentEvent

// localSandbox is the small bundle the runtime needs out of a sandbox: the
// deep-agent SDK client (an always-local one — no container / compose
// paths), the SandboxContext it reports, and the working directory the
// agent operates in. The local SDK client holds no releasable resources
// (no container, no remote connection), so there is no Close.
type localSandbox struct {
	client  sdkSandbox.Sandbox
	ctx     *sbmodel.SandboxContext
	workdir string
}

type Agent struct {
	runner     *adk.Runner
	ctxManager *chatctx.ChatContextManager
	sandbox    *localSandbox
	builder    *round.Builder
	sessionID  string
	// fingerprint is a deterministic hash over the inputs that the
	// constructor bakes into the agent and that cannot be hot-swapped on
	// an existing instance: the model config, the workdir, and the raw
	// system prompt. Callers that cache *Agent instances compare this
	// against a fingerprint computed from the next call's inputs and
	// rebuild the agent on mismatch — without it, switching the session's
	// model would silently keep dispatching to the originally-bound model
	// because the cached *Agent's chatModel is captured at New() time.
	fingerprint string
	// toolFailures is written by the toolWrap middleware whenever a tool
	// endpoint returns an error (the middleware still returns the error
	// text as a success-looking string so the LLM can self-recover), and
	// read-and-consumed by roundAdapter at tool-terminal emission time
	// so the live UI + on-disk history see a ToolCallStatusFailed for
	// the same id. Shared by pointer so a Run's adapter sees writes made
	// during that Run — entries are LoadAndDelete'd on consume, so a
	// stray write with no matching terminal simply stays dormant (new
	// LLM-generated tool_call ids never collide with old ones).
	toolFailures *sync.Map
	mu           sync.RWMutex
	cancel       context.CancelFunc
	cancelGen    uint64 // generation counter to avoid clearing a newer cancel
	// running tracks how many Run() calls are currently in-flight on this
	// agent. Callers caching agents consult IsRunning() before closing an
	// idle entry so a long-lived session whose Run hasn't touched
	// lastAccess recently isn't cancelled mid-stream. Atomic so the
	// reader can read without grabbing mu.
	running atomic.Int32

	// runSem serialises Run() invocations on this Agent. The round.Builder
	// is shared across Runs; without serialisation a new Run's
	// builder.Reset can interleave with an old Run's deferred cleanup
	// (EmitPendingEnds / CollectMessages), corrupting both the live UI
	// stream and the on-disk round. Acquired AFTER cancelling the old
	// Run so the old Run releases the slot promptly via its deferred
	// cleanup; held until the new Run's own cleanup completes.
	//
	// Modeled as a buffered chan struct{} (capacity 1) rather than a
	// sync.Mutex so the acquire is ctx-aware: if the previous Run's
	// detached cleanup (which uses context.WithoutCancel) hangs on a
	// slow disk, the new Run can still observe its own ctx cancellation
	// and return instead of blocking forever.
	runSem chan struct{}
}

type Config struct {
	SystemPrompt   string
	SessionID      string
	SessionDir     string
	SessionToucher chatctx.SessionToucher
}

type Option func(*Config)

func WithSessionID(sessionID string) Option {
	return func(c *Config) {
		c.SessionID = sessionID
	}
}

func WithSystemPrompt(prompt string) Option {
	return func(c *Config) {
		c.SystemPrompt = prompt
	}
}

func WithSessionDir(dir string) Option {
	return func(c *Config) {
		c.SessionDir = dir
	}
}

// WithSessionToucher wires a SessionToucher so BeginRun can bump session
// meta's UpdatedAt through the caller's session writer instead of writing
// the session meta file directly.
func WithSessionToucher(t chatctx.SessionToucher) Option {
	return func(c *Config) {
		c.SessionToucher = t
	}
}

func New(ctx context.Context, workdir string, modelCfg *modelbuilder.ModelConfig, opts ...Option) (*Agent, error) {
	cfg := &Config{}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.SessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if cfg.SessionDir == "" {
		return nil, fmt.Errorf("session dir is required")
	}

	// Always-local sandbox: commands and file operations run directly in
	// workdir on this machine. No container / compose paths.
	sbClient := local.NewClient(local.WithCwd(workdir))

	tools, err := sbClient.MCPTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCP tools: %w", err)
	}

	sbCtx, err := sbClient.GetContext()
	if err != nil {
		return nil, fmt.Errorf("failed to get sandbox context: %w", err)
	}

	sb := &localSandbox{client: sbClient, ctx: sbCtx, workdir: workdir}

	if modelCfg == nil {
		return nil, fmt.Errorf("model config is nil")
	}

	logger.Debugf(ctx, "[eino] build model: class=%s model=%s", modelCfg.ModelClass, modelCfg.Connection.Model)
	chatModel, err := modelbuilder.BuildModel(ctx, modelCfg, modelbuilder.WithLLMMaxTokens(32768))
	if err != nil {
		return nil, fmt.Errorf("failed to create chat model: %w", err)
	}

	chatContextRepo, err := store.NewChatContextRepo(cfg.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create chat context repo: %w", err)
	}

	toolFailures := &sync.Map{}

	handlers, err := middlewares.Init(ctx, &middlewares.InitConfig{
		ChatModel:       chatModel,
		Workspace:       sb.workdir,
		SessionDir:      cfg.SessionDir,
		Sandbox:         sb.client,
		ChatContextRepo: chatContextRepo,
		ToolFailures:    toolFailures,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init middlewares: %w", err)
	}

	systemPrompt := injectEnvPrompt(cfg.SystemPrompt, sb.workdir, sb.ctx)

	agent, err := deep.New(ctx, &deep.Config{
		Name:                   "eino-cli",
		Description:            "an agent for deep task",
		ChatModel:              chatModel,
		Instruction:            systemPrompt,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
		Handlers:               handlers,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools,
			},
		},
		MaxIteration: 100,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})

	chatCtxMgr := chatctx.New(chatContextRepo, cfg.SessionToucher, cfg.SessionID)

	return &Agent{
		runner:       runner,
		sandbox:      sb,
		ctxManager:   chatCtxMgr,
		builder:      round.New(),
		sessionID:    cfg.SessionID,
		toolFailures: toolFailures,
		fingerprint:  computeAgentFingerprint(workdir, modelCfg, cfg.SystemPrompt),
		runSem:       make(chan struct{}, 1),
	}, nil
}

// computeAgentFingerprint hashes the inputs that the constructor bakes
// permanently into the agent (workdir, model config, raw system prompt).
// Callers compare this against a fresh fingerprint at every GetOrCreate to
// detect when a cached agent no longer matches the caller's intent —
// typically because the user switched model on the session — and rebuild.
// Includes the model API key + base URL so re-pointing a logical model id
// to a different upstream still triggers a rebuild. The raw system prompt
// is hashed (not the env-injected version produced inside New) so daily
// changes to the date / timezone stamp do not cause spurious rebuilds.
func computeAgentFingerprint(workdir string, modelCfg *modelbuilder.ModelConfig, systemPrompt string) string {
	h := sha256.New()
	fmt.Fprintf(h, "workdir=%q\n", workdir)
	if modelCfg != nil {
		fmt.Fprintf(h, "class=%s\nthinking=%s\n", modelCfg.ModelClass, modelCfg.ThinkingType)
		if modelCfg.Connection != nil {
			fmt.Fprintf(h, "model=%s\nbase=%s\nkey=%s\n",
				modelCfg.Connection.Model,
				modelCfg.Connection.BaseURL,
				modelCfg.Connection.APIKey)
		}
	}
	fmt.Fprintf(h, "systemPrompt=%s\n", systemPrompt)
	return hex.EncodeToString(h.Sum(nil))
}

// Fingerprint returns the deterministic hash that captures this agent's
// constructor inputs. Callers caching agents use it to invalidate cached
// agents whose inputs (model / workdir / system prompt) no longer match
// the next caller's request.
func (d *Agent) Fingerprint() string {
	return d.fingerprint
}

func (d *Agent) Cancel() {
	d.mu.RLock()
	cancel := d.cancel
	d.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// IsRunning reports whether any Run() call is currently executing on
// this agent. Agent caches check this to avoid closing an agent that's
// still streaming.
func (d *Agent) IsRunning() bool {
	return d.running.Load() > 0
}

// runStart / runEnd bracket a Run() invocation so IsRunning reflects
// in-flight work. Kept as unexported helpers so Run is the only site
// allowed to mutate the counter.
func (d *Agent) runStart() { d.running.Add(1) }
func (d *Agent) runEnd()   { d.running.Add(-1) }

// clearCancel clears the cancel function only if the generation matches,
// preventing a finishing Run from clearing a newer Run's cancel.
func (d *Agent) clearCancel(gen uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancelGen == gen {
		d.cancel = nil
	}
}

// storeCancel sets the cancel function and returns the generation counter
// so the caller can later call clearCancel with the correct generation.
func (d *Agent) storeCancel(cancel context.CancelFunc) uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
	d.cancel = cancel
	d.cancelGen++
	return d.cancelGen
}

// Close cancels any in-flight run and waits for Run() to finish its
// deferred cleanup so the round builder's final flush can reach disk
// before Close returns.
//
// Waiting is bounded so a stuck iterator can't prevent shutdown; if
// the deadline fires we proceed anyway and log — the deferred flush
// uses a detached context so it still persists what it can. The local
// sandbox client holds no releasable resources (no container, no remote
// connection), so there is nothing to release here.
func (d *Agent) Close() {
	d.Cancel()
	d.waitForRunExit(5 * time.Second)
}

// waitForRunExit polls the running counter until it drops to zero or
// the deadline elapses. Polling is fine here: Run() exits quickly once
// runCtx is cancelled, and this path is only hit on shutdown/eviction.
func (d *Agent) waitForRunExit(timeout time.Duration) {
	if d.running.Load() == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if d.running.Load() == 0 {
			return
		}
		if time.Now().After(deadline) {
			logger.Warnf(context.Background(), "[eino] Close timed out waiting for Run to exit: sessionId=%s running=%d", d.sessionID, d.running.Load())
			return
		}
		<-tick.C
	}
}

// stopAndFlushTimeout caps how long StopAndFlush will wait for the Run's
// deferred cleanup to drain. Generous enough to cover Run's deferred
// onFlush (bounded by round.PersistTimeout = 10s) plus a small slack;
// after this we fall back to an inline flush so the builder's
// accumulators still reach disk.
const stopAndFlushTimeout = 12 * time.Second

// StopAndFlush atomically cancels any in-flight Run and ensures the
// builder's accumulated state reaches disk. Replaces the prior
// FlushPendingMessages -> Cancel idiom from cleanup callers, which had
// a real race window: FlushPendingMessages emitted pending-end events
// and cleared the builder's accumulators, but the Run was still active
// and could push new stream events into the just-cleared builder. The
// late events would be dropped (no matching round / no terminal site)
// and a real tool terminal arriving after the eager superseded flush
// would never reach disk — only the canceled placeholder would.
//
// New order:
//  1. Cancel runCtx so the Run loop exits ASAP.
//  2. Wait (bounded by stopAndFlushTimeout) for Run to finish its
//     deferred cleanup chain — that chain runs round.FinalizeRound
//     while onFlush is still installed, so any in-flight round is
//     persisted atomically with the cancel.
//  3. As a safety net, run FlushPendingMessages. If Run already
//     drained the builder, this is a no-op (CollectMessages returns
//     nothing). If Run hung past the timeout, this still gets the
//     builder's accumulators onto disk via the inline persist
//     callback that runs because onFlush has been ClearOnFlush'd.
func (d *Agent) StopAndFlush() {
	d.Cancel()
	d.waitForRunExit(stopAndFlushTimeout)
	d.FlushPendingMessages()
}

// FlushPendingMessages drains the round builder and persists any round
// still in flight. Delegates to round.FlushPending, the shared cleanup
// path. The persist call is wrapped in
// round.PersistContext so the canonical persist deadline applies — see
// round.PersistTimeout for the scope of what that deadline currently
// covers (lock-wait yes, blocking file I/O no).
func (d *Agent) FlushPendingMessages() {
	round.FlushPending(d.builder, func(msgs []*schema.Message) error {
		ctx, cancel := round.PersistContext(context.Background())
		defer cancel()
		return d.ctxManager.AppendMessages(ctx, msgs...)
	}, fmt.Sprintf("eino sessionId=%s", d.sessionID))
}

func injectEnvPrompt(systemPrompt string, workspace string, sandboxCtx *sbmodel.SandboxContext) string {
	now := time.Now()
	// Some code paths (local sandbox without an explicit GetContext, tests)
	// may pass nil. Fall back to runtime detection rather than panicking.
	osName := runtime.GOOS
	arch := runtime.GOARCH
	if sandboxCtx != nil {
		if sandboxCtx.OS != "" {
			osName = sandboxCtx.OS
		}
		if sandboxCtx.Arch != "" {
			arch = sandboxCtx.Arch
		}
	}
	return fmt.Sprintf(`%s
<env>
Operating system: %s
Working directory: %s
Today's date: %s
Timezone: %s
Architecture: %s
</env>
`, systemPrompt,
		osName,
		workspace,
		now.Format("2006-01-02"),
		now.Location().String(),
		arch)
}
