package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acp "github.com/eino-contrib/acp"
	acpconn "github.com/eino-contrib/acp/conn"
	"github.com/eino-contrib/acp/transport/stdio"

	"github.com/fanlv/quartet/pkg/json"
	"github.com/fanlv/quartet/pkg/logger"
)

const (
	// connCreateTimeout bounds how long ACP subprocess startup and handshake
	// can take before we abort the attempt.
	connCreateTimeout = 60 * time.Second

	// gracefulShutdownTimeout is how long we wait after SIGTERM before sending SIGKILL.
	gracefulShutdownTimeout = 5 * time.Second

	// connIdleTimeout is how long an unused connection can stay alive
	// before the idle reaper closes it and kills the subprocess.
	connIdleTimeout = 30 * time.Minute

	// idleReapInterval is how often the idle reaper scans tracked connections.
	idleReapInterval = 1 * time.Minute

	// touchLastUsedThreshold skips redundant lastUsedAt writes if the
	// connection was already touched within this window.
	touchLastUsedThreshold = 5 * time.Minute

	// cancelActivePromptTimeout caps how long we will wait for the ACP
	// subprocess to acknowledge a SessionCancel before giving up. Without
	// this, AcquirePromptSlot's pre-cancel could block on a stuck stdio
	// pipe and freeze Stop / run-switch indefinitely.
	cancelActivePromptTimeout = 2 * time.Second
)

// Conn is a live connection to an ACP agent subprocess. It owns the child
// process, captures its stderr, and serializes Prompt calls across sessions
// (the subprocess supports one active Prompt at a time).
type Conn struct {
	// --- Process lifecycle ---
	cmd       *exec.Cmd
	stderrBuf *syncBuffer

	// processDone is closed by exactly one goroutine that owns cmd.Wait().
	// Relying on platform probes (kill(pid, 0), tasklist, etc.) is not enough:
	// a crashed-but-unwaited child can still look alive on Unix, and tasklist's
	// exit status is not a reliable existence signal on Windows.
	processDone chan struct{}

	// --- RPC layer ---
	conn   *acpconn.ClientConnection
	client *sdkClient

	// --- Prompt serialization ---
	// promptSem serializes Prompt calls on this connection.
	promptSem chan struct{}

	// activeSessionMu protects activeSessionID, used to cancel the running
	// prompt before a new one starts.
	activeSessionMu sync.Mutex
	activeSessionID acp.SessionID

	// --- Idle management & use tracking ---
	lastUsedAt time.Time
	lastUsedMu sync.RWMutex
	useMu      sync.Mutex
	activeUses int

	// --- Flags ---
	// closedByIdleReapFlag records whether Close was triggered by the
	// idle reaper (expected lifecycle) vs. some other reason (OOM /
	// explicit shutdown / crash). The reconnect path in the ACP agent
	// reads this to pick the right log level — a reaper-triggered
	// reconnect is not an error and must not pollute WARN aggregates.
	closedByIdleReapFlag atomic.Bool

	// supportsResume records whether the agent advertised the
	// sessionCapabilities.resume capability during initialize. Set once
	// right after the handshake (before the Conn is handed to callers, so
	// no synchronization is needed) and read by the reconnect path to
	// choose session/resume over session/load. session/resume restores a
	// session WITHOUT replaying history via session/update, which avoids
	// the duplicate-message bug where load-time replay events are treated
	// as freshly generated output and re-persisted / re-pushed.
	supportsResume bool
}

// Pid returns the subprocess PID, or 0 if the process is not running.
func (c *Conn) Pid() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// Stderr returns the captured subprocess stderr.
func (c *Conn) Stderr() string {
	return c.stderrBuf.String()
}

// IsAlive reports whether the connection is still usable. A connection that
// has already been selected by the idle reaper is treated as not alive even if
// its subprocess has not finished terminating yet, so callers won't race into
// a Conn that is about to be closed outside the pool lock.
func (c *Conn) IsAlive() bool {
	if c.ClosedByIdleReap() {
		return false
	}
	return c.isAlive()
}

// isAlive reports whether the subprocess Wait goroutine has observed process
// exit. It intentionally does not shell out to platform-specific process
// listing tools: Wait is the only authoritative state for our own child.
func (c *Conn) isAlive() bool {
	if c == nil || c.cmd == nil || c.cmd.Process == nil || c.processDone == nil {
		return false
	}
	select {
	case <-c.processDone:
		return false
	default:
		return true
	}
}

func (c *Conn) startProcessMonitor() {
	go func() {
		_ = c.cmd.Wait()
		close(c.processDone)
	}()
}

func (c *Conn) waitForProcessExit(timeout time.Duration) bool {
	if c == nil || c.processDone == nil {
		return true
	}
	if timeout <= 0 {
		select {
		case <-c.processDone:
			return true
		default:
			return false
		}
	}
	select {
	case <-c.processDone:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Close tears down the subprocess. It does not untrack the Conn from the
// idle-reaper list — dead connections are pruned on the next reap cycle.
func (c *Conn) Close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.terminate()
}

// MarkClosedByIdleReap records that this connection is being closed by
// the idle reaper. The next reconnect attempt uses this to downgrade its
// "subprocess dead" log from WARN to INFO, so an expected idle recycle
// doesn't look like a crash in log aggregates. Must be called BEFORE
// Close so the flag is visible if the reap and reconnect race.
func (c *Conn) MarkClosedByIdleReap() {
	c.closedByIdleReapFlag.Store(true)
}

// ClosedByIdleReap reports whether this connection's Close was triggered
// by the idle reaper. Returns false for any other cause (explicit
// shutdown, subprocess crash, OOM, handshake error).
func (c *Conn) ClosedByIdleReap() bool {
	return c.closedByIdleReapFlag.Load()
}

// TouchLastUsed refreshes lastUsedAt to prevent the idle reaper from closing
// this connection. Called by the service layer on each user message and by
// the internal update dispatcher on every session update.
func (c *Conn) TouchLastUsed() {
	elapsed := time.Since(c.LastUsedAt())
	if elapsed < touchLastUsedThreshold {
		return
	}
	c.setLastUsedAt(time.Now())
}

// TryAcquireUse claims this connection for a caller that is about to issue
// RPCs to the subprocess. It closes the race where the idle reaper selects an
// idle-but-live Conn for teardown while a new Run is starting on that same
// Conn. The reaper checks the same useMu before marking/closing candidates.
func (c *Conn) TryAcquireUse() bool {
	c.useMu.Lock()
	defer c.useMu.Unlock()

	if c.ClosedByIdleReap() || !c.isAlive() {
		return false
	}
	c.activeUses++
	c.setLastUsedAt(time.Now())
	return true
}

// ReleaseUse releases a prior successful TryAcquireUse call.
func (c *Conn) ReleaseUse() {
	c.useMu.Lock()
	if c.activeUses > 0 {
		c.activeUses--
	}
	c.useMu.Unlock()
}

// tryBeginIdleReap marks this connection for idle reaping if it is currently
// unused and past connIdleTimeout. The caller must remove marked connections
// from trackedConns and close them after releasing trackedConnsMu.
func (c *Conn) tryBeginIdleReap(now time.Time) (idle time.Duration, reap bool) {
	c.useMu.Lock()
	defer c.useMu.Unlock()

	if c.activeUses > 0 || c.ClosedByIdleReap() {
		return 0, false
	}
	if !c.isAlive() {
		return 0, true
	}
	lastUsedAt := c.LastUsedAt()
	idle = now.Sub(lastUsedAt)
	if idle <= connIdleTimeout {
		return 0, false
	}

	// Mark BEFORE Close so the reconnect path sees the flag even if it races
	// our close — the reconnect only reads the flag after it has already
	// observed a dead/unusable conn.
	c.MarkClosedByIdleReap()
	return idle, true
}

// LastUsedAt returns the timestamp of the last activity on this connection.
func (c *Conn) LastUsedAt() time.Time {
	c.lastUsedMu.RLock()
	defer c.lastUsedMu.RUnlock()
	return c.lastUsedAt
}

func (c *Conn) setLastUsedAt(t time.Time) {
	c.lastUsedMu.Lock()
	c.lastUsedAt = t
	c.lastUsedMu.Unlock()
}

// CancelActivePrompt sends a Cancel notification for whichever session is
// currently holding the prompt slot on this connection, if any. The caller
// supplies a context so this RPC cannot block Stop / run-switch on a stuck
// subprocess; pass a context.WithTimeout(WithoutCancel(parent), ...) when
// you want bounded waits even if the parent has already been cancelled.
func (c *Conn) CancelActivePrompt(ctx context.Context) {
	c.activeSessionMu.Lock()
	sid := c.activeSessionID
	c.activeSessionMu.Unlock()

	if sid != "" {
		_ = c.conn.SessionCancel(ctx, acp.CancelNotification{SessionID: sid})
	}
}

func (c *Conn) setActiveSession(sid acp.SessionID) {
	c.activeSessionMu.Lock()
	c.activeSessionID = sid
	c.activeSessionMu.Unlock()
}

// clearActiveSession clears the active session record, but only if it still
// matches the given session (avoids clearing a newer session's record).
func (c *Conn) clearActiveSession(sid acp.SessionID) {
	c.activeSessionMu.Lock()
	if c.activeSessionID == sid {
		c.activeSessionID = ""
	}
	c.activeSessionMu.Unlock()
}

// NewTrackedConn creates a new Conn with the standard startup timeout and
// registers it for idle reaping. Every caller path (service agent, probe)
// should go through this helper.
func NewTrackedConn(ctx context.Context, agentType, workdir string) (*Conn, error) {
	logger.Debugf(ctx, "[ACP] creating Conn for agentType=%s workdir=%s", agentType, workdir)
	if isConnPoolClosing() {
		return nil, errConnPoolClosing
	}

	// Detach direct caller cancellation because tracked connections are often
	// built inside a shared singleflight: one waiter going away must not abort
	// creation for every other waiter. Preserve an earlier caller deadline,
	// though, so a recovery chain with a finite total budget cannot accidentally
	// start a brand-new 60s handshake after most of that budget is already gone.
	createTimeout := connCreateTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		if remaining < createTimeout {
			createTimeout = remaining
		}
	}
	createCtx, createCancel := context.WithTimeout(context.WithoutCancel(ctx), createTimeout)
	defer createCancel()

	conn, err := NewConn(createCtx, agentType, workdir)
	if err != nil {
		return nil, err
	}
	if err := trackConn(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// NewProbeConn creates a short-lived Conn for capability probing.
//
// Unlike NewTrackedConn it does NOT detach from the caller's cancellation
// signal. A probe deliberately sets a tight deadline (see
// services/agent/probe.acpProbeTimeout) that must bound BOTH subprocess
// startup + initialize handshake AND the throwaway session/new that follows,
// so an agent that is installed but not logged in — and therefore never
// answers session/new — can't wedge the probe or the /agent/list request that
// triggered it. NewTrackedConn preserves an earlier deadline but deliberately
// ignores direct cancellation for shared creation; a probe must honor both.
//
// The returned Conn is not registered for idle reaping: probe callers Close it
// immediately after reading its session info.
func NewProbeConn(ctx context.Context, agentType, workdir string) (*Conn, error) {
	if isConnPoolClosing() {
		return nil, errConnPoolClosing
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, connCreateTimeout)
		defer cancel()
	}
	return NewConn(ctx, agentType, workdir)
}

// NewConn starts an ACP agent subprocess and completes the initialize
// handshake. Caller is responsible for tracking the returned Conn for idle
// reaping if desired; most callers should use NewTrackedConn instead.
func NewConn(ctx context.Context, agentType, workdir string) (*Conn, error) {
	parts := strings.Fields(agentType)
	if len(parts) == 0 {
		return nil, fmt.Errorf("agentType is empty")
	}
	if !IsAllowedAgentCommand(agentType) {
		return nil, fmt.Errorf("agentType %q is not in the allowed list; allowed commands: %v", agentType, AllowedAgentCommands())
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Env = append(os.Environ(), ACPChildMarkerEnv+"="+ACPChildMarkerValue)
	cmd.SysProcAttr = sysProcAttr()

	if extras := resolveExtraEnv(agentType); len(extras) > 0 {
		var count int
		for _, e := range extras {
			if e.Key == "" {
				continue
			}
			cmd.Env = append(cmd.Env, e.Key+"="+e.Value)
			count++
		}
		if count > 0 {
			logger.Debugf(ctx, "[ACP] injecting %d env vars for agent %s", count, agentType)
		}
	}

	if workdir != "" {
		cmd.Dir = workdir
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf syncBuffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("failed to start acp agent: %w", err)
	}

	client := newSDKClient()

	// stdio.NewTransport takes (reader, writer): reader = subprocess stdout,
	// writer = subprocess stdin. Wrap with fixLineTypeTransport to repair
	// known type mismatches (e.g. ToolCallLocation.line sent as string).
	//
	// The stdio transport reads newline-delimited JSON via bufio.Scanner with
	// a per-message size cap (SDK default 10MB). When an agent returns a very
	// large single message (e.g. a tool result carrying big file contents or
	// base64 payloads), the scanner exceeds that cap and fails with
	// "bufio.Scanner: token too long", tearing down the connection. Raise the
	// cap to 64MB to tolerate large tool results — quartet runs single-user on
	// a personal machine / sandbox, so the memory tradeoff is acceptable.
	rawTransport := stdio.NewTransport(stdout, stdin, stdio.WithMaxMessageSize(64*1024*1024))
	transport := newFixLineTypeTransport(rawTransport, agentType)
	sdkConn := acpconn.NewClientConnection(client, transport,
		acpconn.WithNotificationErrorHandler(func(method string, err error) {
			logger.Errorf(context.Background(),
				"[ACP] notification unmarshal failed: method=%s err=%v", method, err)
		}),
	)

	c := &Conn{
		conn:        sdkConn,
		cmd:         cmd,
		client:      client,
		stderrBuf:   &stderrBuf,
		processDone: make(chan struct{}),
		lastUsedAt:  time.Now(),
		promptSem:   make(chan struct{}, 1),
	}
	client.onActivity = c.TouchLastUsed
	c.startProcessMonitor()

	// Start the JSON-RPC I/O loop before any outbound call. This must
	// happen inside a lifetime context; we use Background so the loop
	// is torn down only by c.conn.Close() in Conn.Close().
	if err := sdkConn.Start(context.Background()); err != nil {
		_ = sdkConn.Close()
		_ = cmd.Process.Kill()
		c.waitForProcessExit(gracefulShutdownTimeout)
		return nil, fmt.Errorf("acp start failed: %w, stderr: %s", err, stderrBuf.String())
	}

	initResp, err := sdkConn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion(acp.CurrentProtocolVersion),
		ClientInfo:      &acp.Implementation{Name: "quartet", Version: "0.1.0"},
	})
	if err != nil {
		_ = sdkConn.Close()
		_ = cmd.Process.Kill()
		c.waitForProcessExit(gracefulShutdownTimeout)
		return nil, fmt.Errorf("acp initialize failed: %w, stderr: %s", err, stderrBuf.String())
	}
	logger.Debugf(ctx, "[ACP] connected to agentType=%s initResp=%s", agentType, json.String(initResp))

	// Record resume support so the reconnect path can prefer session/resume
	// (no history replay) over session/load (replays history via
	// session/update, which our stream handler would mis-attribute as new
	// output). The capability uses {}-as-presence semantics: a non-nil
	// Resume pointer means the agent supports session/resume.
	if caps := initResp.AgentCapabilities; caps != nil && caps.SessionCapabilities != nil {
		c.supportsResume = caps.SessionCapabilities.Resume != nil
	}
	logger.Infof(ctx, "[ACP] capabilities: agentType=%s loadSession=%v resume=%v",
		agentType, initResp.AgentCapabilities != nil && initResp.AgentCapabilities.LoadSession, c.supportsResume)

	return c, nil
}
