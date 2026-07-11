package acp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/schema"
	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/pkg/tokenizer"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/chatctx"
	"github.com/fanlv/quartet/services/agent/internal/acpstate"
	"github.com/fanlv/quartet/services/agent/round"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// tokenUsageMinInterval throttles the per-flush local token recompute used
// as a fallback when the ACP subprocess does not emit usage_update during a
// turn. Each recompute reloads the full history and tokenises it, so on long
// turns doing it per flush would approach O(history * flushCount); debounce
// so the UI still gets a live count without redundant work. Mirrors the eino
// path (services/agent/eino/runner.go).
const tokenUsageMinInterval = 1 * time.Second

// SessionStore is the narrow session.Service surface the ACP agent needs
// for loading/writing session metadata. Declared here (rather than
// importing services/session directly) to keep the ACP package decoupled
// from the session package wiring.
type SessionStore interface {
	Get(sessionID string) (*model.Session, bool)
	UpdateACPState(sessionID, acpSessionID string, fingerprint repository.MessagesFingerprint) error
	UpdateACPSyncFingerprint(sessionID string, fingerprint repository.MessagesFingerprint) error
	Touch(sessionID string) error
}

// ACPAgent wraps a pkg/acp connection + session with the quartet
// business concerns: chat context persistence, message round aggregation,
// and transparent reconnect when the subprocess dies.
//
// Concurrency model: conn/acpSession/currentModelID/currentMode can mutate
// mid-lifetime (reconnectIfNeeded, resetACPSession, UpdateACP*), while
// Cancel/Close may fire from an external goroutine (user hits stop, session
// cleanup). All reads and writes of those fields go through a.mu; long I/O
// runs against a snapshot taken under the lock so we never block the
// subprocess on a pipe write while holding it.
type ACPAgent struct {
	ctxManager *chatctx.ChatContextManager
	builder    *round.Builder

	mu         sync.RWMutex
	conn       *pkgacp.Conn
	acpSession pkgacp.SessionID
	cancel     context.CancelFunc
	cancelGen  uint64 // generation counter to avoid clearing a newer cancel
	// currentModelID/currentMode are the last values pushed to the
	// subprocess session via SetSessionModel/SetSessionMode. Kept to skip
	// redundant calls on successive Runs against the same selection.
	currentModelID string
	currentMode    string
	// currentThoughtLevel is the last thought_level value pushed to the
	// subprocess session via SetSessionThoughtLevel. Same skip-redundant
	// semantics as currentModelID/currentMode.
	currentThoughtLevel string
	// thoughtLevelConfigID is the ACP config option id (e.g.
	// "reasoning_effort") discovered from the session's ConfigOptions.
	// thought_level has no dedicated RPC, so UpdateACPThoughtLevel drives it
	// through SetSessionConfigOption keyed by this id. Empty when the agent
	// does not advertise a thought_level option.
	thoughtLevelConfigID string

	// Stored for reconnection when the underlying process dies.
	agentType string
	workdir   string

	// sessionStore drives all session-metadata writes (ACPSessionID,
	// ACPLastSyncedMessageCount) through a single in-memory+disk writer,
	// so ACP writes can't race with parallel model/title/touch writers
	// and lose field updates. sessionID is the quartet-level session id
	// (meta key), distinct from acpSession (subprocess-level id).
	sessionStore SessionStore
	sessionID    string

	// lastSyncedFingerprint is the messages.jsonl fingerprint at the
	// end of this agent's most recent Run (or at the moment the
	// subprocess session was (re)created for an untouched session).
	// Used at the start of the next Run to detect cross-path drift: if
	// the current disk fingerprint differs, something outside this ACP
	// session wrote to disk (eino Run, summary compression, late tool
	// stitch via ReplacePlaceholderToolResult, external edit) and the
	// subprocess's internal conversation no longer matches. Persisted
	// as Session.ACPLastSyncedMessageCount + ACPLastSyncedMessageHash so
	// drift is detectable even across server restarts.
	//
	// Hash is essential — count alone misses in-place row rewrites
	// (ReplacePlaceholderToolResult, hand edits that swap content for
	// equal-length content) that the prior count-only check let through.
	//
	// Stored under fingerprintMu rather than as atomics because Hash is
	// a string: pairing the count update with a hash update under the
	// same mutex keeps the two fields consistent for the next drift
	// check (a torn read with the new count and the old hash would
	// either miss real drift or force a spurious reset).
	fingerprintMu         sync.Mutex
	lastSyncedFingerprint repository.MessagesFingerprint

	// needReplay is set whenever the subprocess session was freshly
	// minted while quartet's messages.jsonl already held prior
	// turns — cold-start drift, per-Run drift, BeginRun truncate, or
	// LoadSession failure on an existing id. A fresh ACP session holds
	// zero conversation memory, so the next Run must prepend the
	// prior history (summary + messages) as a text context block
	// ahead of the user's current turn; otherwise the model answers
	// as if this were a brand-new conversation and silently forgets
	// everything written to disk before the reset. Cleared only after
	// SendPrompt succeeds — a failed / cancelled SendPrompt leaves the
	// flag set so the retry gets the same replay.
	needReplay atomic.Bool

	// needSessionReset is set when a Run is cancelled (user Stop /
	// timeout) after the cancel notification has been sent to the
	// subprocess. Some ACP backends (notably @zed-industries/claude-agent)
	// leave the session in a "tainted" state after SessionCancel — the
	// next Prompt on the same session returns immediately with no
	// content. Setting this flag forces the next Run to create a fresh
	// subprocess session (via resetACPSession) before sending its prompt,
	// sidestepping the tainted-session problem entirely.
	needSessionReset atomic.Bool

	// running counts in-flight Run() invocations. LRU eviction in the
	// service layer consults IsRunning() to skip agents that are actively
	// streaming — evicting one mid-run would close the subprocess conn
	// under the goroutine and surface as an opaque stream error.
	running atomic.Int64

	// runSem serialises Run() invocations on this Agent. The round.Builder
	// and the conn-level stream handler are shared across Runs; without
	// serialisation a new Run's builder.Reset / SetStreamHandler can
	// interleave with an old Run's deferred cleanup (EmitPendingEnds /
	// CollectMessages / ClearStreamHandlerIfGen) or with stream events
	// still routing to the old handler, corrupting both the live UI
	// stream and the on-disk round. Acquired AFTER signalling the old
	// Run to cancel so the old Run releases the slot promptly via its
	// deferred cleanup; held until the new Run's own cleanup completes.
	//
	// Modeled as a buffered chan struct{} (capacity 1) rather than a
	// sync.Mutex so the acquire is ctx-aware: if the previous Run's
	// detached cleanup (which uses context.WithoutCancel) hangs on a
	// slow disk or remote sandbox, the new Run can still observe its
	// own ctx cancellation and return instead of blocking forever.
	runSem chan struct{}
}

// thoughtLevelConfigIDFromSession returns the thought_level config option id
// (e.g. "reasoning_effort") advertised by the session response, or "" when
// the agent does not expose a thought_level option.
func thoughtLevelConfigIDFromSession(resp *pkgacp.SessionResponse) string {
	if resp == nil {
		return ""
	}
	if sel := resp.ThoughtLevelConfigSelect(); sel != nil {
		return sel.ConfigID
	}
	return ""
}

// NewACPAgent starts an ACP agent subprocess (via pkg/acp), creates or
// reloads the ACP session for this quartet session, and prepares the
// chat context manager so Run() can stream events.
func NewACPAgent(ctx context.Context, store SessionStore, sessionID, agentType, workdir, jobID, wsID string) (_ *ACPAgent, retErr error) {
	logger.Debugf(ctx, "[acp] new agent: sessionId=%s type=%s workdir=%s jobId=%s", sessionID, agentType, workdir, jobID)

	conn, err := pkgacp.NewTrackedConn(ctx, agentType, workdir)
	if err != nil {
		return nil, err
	}
	// Close the subprocess connection on any setup error below; otherwise
	// the tracked Conn would sit orphaned until the idle reaper gets to it.
	defer func() {
		if retErr != nil {
			conn.Close()
		}
	}()

	existingACPSession, persistedFingerprint, err := loadPersistedACPState(store, sessionID)
	if err != nil {
		// Distinguishing "storage read failed" from "no prior session"
		// matters: silently falling through to NewSession on storage
		// read failure would mask a real disk / serialization problem
		// and orphan the previous ACP session id.
		return nil, fmt.Errorf("load persisted ACP session id failed: %w", err)
	}

	chatContextRepo, err := repository.NewChatContextRepo(wsID, jobID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to create chat context repo: %w", err)
	}

	ctxMgr := chatctx.New(chatContextRepo, store, sessionID)

	// Detect cross-path drift at cold start: if messages.jsonl has moved
	// since the last ACP Run ended, the subprocess's view stored under
	// existingACPSession no longer matches disk. Discard the old id and
	// let the NewSession branch below mint a fresh one. Fingerprint
	// failure is fatal here: we cannot decide drift vs. no-drift without
	// reading disk, and silently treating failure as the zero
	// fingerprint would masquerade an I/O error as "empty history" and
	// force a spurious subprocess reset.
	currentFingerprint, err := ctxMgr.MessagesFingerprint(ctx)
	if err != nil {
		return nil, fmt.Errorf("compute messages fingerprint for drift check failed: %w", err)
	}
	if existingACPSession != "" && !persistedFingerprint.Equal(currentFingerprint) {
		logger.Warnf(ctx, "[acp] cross-path drift detected on cold start: persisted=(count=%d hash=%s) current=(count=%d hash=%s) acpSession=%s — discarding stale id",
			persistedFingerprint.Count, persistedFingerprint.Hash,
			currentFingerprint.Count, currentFingerprint.Hash,
			existingACPSession)
		existingACPSession = ""
	}

	var acpSessionID pkgacp.SessionID
	// thoughtLevelConfigID is captured from whichever session response we
	// end up using (resume / load / new) so UpdateACPThoughtLevel can later
	// target the right config option. thought_level has no dedicated RPC.
	var thoughtLevelConfigID string
	// createdFreshSession tracks whether we ended up calling NewSession
	// (either because no persisted id existed, drift discarded it, or
	// LoadSession failed on the existing id). In all three paths the
	// subprocess holds zero conversation memory, so if messages.jsonl
	// is non-empty the next Run must replay that history as a text
	// prefix.
	createdFreshSession := false
	if existingACPSession != "" {
		acpSessionID = pkgacp.SessionID(existingACPSession)
		// Prefer session/resume (no history replay) over session/load
		// when the agent supports it, mirroring reconnectIfNeeded. On
		// cold start the per-Run stream handler is not installed yet, so
		// load-time replay events are usually dropped — but resume avoids
		// emitting them at all, which is cleaner and removes any reliance
		// on that timing. Fall back to LoadSession if resume is
		// unavailable or fails.
		restored := false
		if conn.SupportsResume() {
			if sessResp, err := conn.ResumeSession(ctx, existingACPSession, workdir); err != nil {
				logger.Warnf(ctx, "[acp] ResumeSession failed on cold start, falling back to LoadSession: acpSession=%s err=%v", existingACPSession, err)
			} else {
				thoughtLevelConfigID = thoughtLevelConfigIDFromSession(sessResp)
				restored = true
			}
		}
		if !restored {
			if sessResp, err := conn.LoadSession(ctx, existingACPSession, workdir); err != nil {
				logger.Errorf(ctx, "[acp] LoadSession failed, creating new: acpSession=%s err=%v", existingACPSession, err)
				existingACPSession = ""
			} else {
				thoughtLevelConfigID = thoughtLevelConfigIDFromSession(sessResp)
			}
		}
	}

	if existingACPSession == "" {
		sessResp, err := conn.NewSession(ctx, workdir)
		if err != nil {
			return nil, err
		}
		acpSessionID = pkgacp.SessionID(sessResp.SessionID)
		thoughtLevelConfigID = thoughtLevelConfigIDFromSession(sessResp)
		createdFreshSession = true

		// Freshly-minted subprocess session: its "synced point" is the
		// current disk snapshot. Persist both id and fingerprint
		// atomically so the next restart sees a consistent pair.
		//
		// fail-fast on persist error: if this is a "replacement" session
		// (cold-start drift discarded the old id, or LoadSession failed
		// on it) the old ACPSessionID is still on disk. Continuing in
		// memory with the new id while disk holds the old one + new
		// fingerprint would, after a restart, route the next Run to
		// LoadSession(stale id) — which silently diverges or fails to
		// load history. Refusing to construct the agent surfaces the
		// underlying disk problem to the caller and lets the next
		// agent build retry from a consistent point.
		if err := savePersistedACPState(ctx, store, sessionID, sessResp.SessionID, currentFingerprint); err != nil {
			return nil, fmt.Errorf("persist acp session state: %w", err)
		}
		persistedFingerprint = currentFingerprint
	}
	logger.Infof(ctx, "[acp] agent ready: sessionId=%s acpSession=%s syncedCount=%d syncedHash=%s", sessionID, acpSessionID, persistedFingerprint.Count, persistedFingerprint.Hash)

	agent := &ACPAgent{
		conn:                 conn,
		acpSession:           acpSessionID,
		ctxManager:           ctxMgr,
		builder:              round.New(),
		agentType:            agentType,
		workdir:              workdir,
		thoughtLevelConfigID: thoughtLevelConfigID,
		sessionStore:         store,
		sessionID:            sessionID,
		runSem:               make(chan struct{}, 1),
	}
	agent.storeFingerprint(persistedFingerprint)
	// A fresh subprocess session has no conversation memory. If disk
	// already holds prior turns, the next Run must replay them — else
	// the model answers as if the conversation just started. A loaded
	// session already carries its own history, so leave the flag false.
	if createdFreshSession && currentFingerprint.Count > 0 {
		agent.needReplay.Store(true)
	}
	return agent, nil
}

// loadPersistedACPState reads the persisted ACP session id and last-synced
// fingerprint from session meta via the in-memory session store. Returns
// ("", zero fingerprint, nil) when the session meta exists but
// ACPSessionID is empty (never been started under ACP).
func loadPersistedACPState(store SessionStore, sessionID string) (string, repository.MessagesFingerprint, error) {
	if store == nil {
		return "", repository.MessagesFingerprint{}, nil
	}
	s, ok := store.Get(sessionID)
	if !ok {
		return "", repository.MessagesFingerprint{}, fmt.Errorf("session %s not found in store", sessionID)
	}
	return s.ACPSessionID, repository.MessagesFingerprint{
		Count: s.ACPLastSyncedMessageCount,
		Hash:  s.ACPLastSyncedMessageHash,
	}, nil
}

// savePersistedACPState atomically records both the ACP session id and the
// fingerprint under which it is valid. Writing them together avoids a
// state where meta has a new id but a stale fingerprint (or vice versa),
// which would confuse the drift check on the next Run.
func savePersistedACPState(ctx context.Context, store SessionStore, sessionID, acpSessionID string, fp repository.MessagesFingerprint) error {
	if store == nil {
		return nil
	}
	if err := store.UpdateACPState(sessionID, acpSessionID, fp); err != nil {
		return err
	}
	logger.Debugf(ctx, "[acp] persisted session state: sessionId=%s acpSession=%s syncedCount=%d syncedHash=%s", sessionID, acpSessionID, fp.Count, fp.Hash)
	return nil
}

// savePersistedACPSyncFingerprint updates only the sync fingerprint on
// session meta — used at the end of a Run once everything is flushed,
// when the id hasn't changed. Kept as a narrow helper to avoid an
// unnecessary full-record overwrite at every Run boundary.
func savePersistedACPSyncFingerprint(ctx context.Context, store SessionStore, sessionID string, fp repository.MessagesFingerprint) error {
	if store == nil {
		return nil
	}
	if err := store.UpdateACPSyncFingerprint(sessionID, fp); err != nil {
		return err
	}
	logger.Debugf(ctx, "[acp] persisted sync fingerprint: sessionId=%s syncedCount=%d syncedHash=%s", sessionID, fp.Count, fp.Hash)
	return nil
}

// loadFingerprint returns the in-memory baseline under fingerprintMu.
// Read-only callers (drift check, log lines) use this; writers use
// storeFingerprint to keep count + hash consistent.
func (a *ACPAgent) loadFingerprint() repository.MessagesFingerprint {
	a.fingerprintMu.Lock()
	defer a.fingerprintMu.Unlock()
	return a.lastSyncedFingerprint
}

// storeFingerprint atomically replaces the in-memory baseline under
// fingerprintMu. Pairing count and hash under one mutex keeps the
// drift-check observation consistent: without it a writer that
// updated count first and hash second could be observed mid-update by
// the drift check, producing a fingerprint with the new count and the
// old hash that neither matches the previous state (false drift) nor
// the new state (real drift missed).
func (a *ACPAgent) storeFingerprint(fp repository.MessagesFingerprint) {
	a.fingerprintMu.Lock()
	a.lastSyncedFingerprint = fp
	a.fingerprintMu.Unlock()
}

// updateSyncBaseline records the post-Run sync point. Called from the
// deferred finalize block in Run AFTER CollectMessages drains the builder
// through onFlush, so messages.jsonl reflects the rounds the subprocess
// just emitted. ctx is expected to be detached (context.WithoutCancel) so
// the count + persist run to completion even on the user-interrupt path.
//
// persistErr captures any incremental-persist failure observed during the
// Run (onFlush failure, ReplacePlaceholderToolResult failure, or a
// failure during the final CollectMessages flush). When non-nil, the
// baseline must NOT be advanced: messages.jsonl is missing rounds the
// subprocess already saw, and recording "we are in sync" against current
// disk count would hide that gap from the next Run's drift check
// (currentCount == syncedCount → no reset) and let the subprocess's
// in-memory history diverge silently from disk. Set needReplay so the
// next Run rebuilds context from disk; if the missing rows actually
// shifted the count, the drift check will also fire reset, which is the
// desired recovery.
//
// On fingerprint failure (non-nil err from MessagesFingerprint), leave
// lastSyncedFingerprint untouched rather than overwriting it with the
// zero value. Tainting the baseline with a fabricated zero would make
// the next Run's drift check see "real fingerprint != zero" and force a
// spurious subprocess reset on every subsequent turn until the I/O
// issue clears. A stale-but-previously-correct baseline is the
// least-bad fallback: at worst it yields one defensive reset on the
// next Run.
func (a *ACPAgent) updateSyncBaseline(ctx context.Context, persistErr error) {
	if persistErr != nil {
		a.needReplay.Store(true)
		prev := a.loadFingerprint()
		logger.Warnf(ctx, "[acp] skip sync baseline update: persist failed, sessionId=%s lastSyncedCount=%d lastSyncedHash=%s err=%v", a.sessionID, prev.Count, prev.Hash, persistErr)
		return
	}

	fp, err := a.ctxManager.MessagesFingerprint(ctx)
	if err != nil {
		prev := a.loadFingerprint()
		logger.Errorf(ctx, "[acp] post-run fingerprint compute failed, keeping previous sync baseline: sessionId=%s lastSyncedCount=%d lastSyncedHash=%s err=%v", a.sessionID, prev.Count, prev.Hash, err)
		return
	}
	a.storeFingerprint(fp)
	if a.sessionStore == nil || a.sessionID == "" {
		return
	}
	if err := savePersistedACPSyncFingerprint(ctx, a.sessionStore, a.sessionID, fp); err != nil {
		logger.Warnf(ctx, "[acp] persist sync fingerprint failed: sessionId=%s err=%v", a.sessionID, err)
	}
}

// snapshotTransport returns a consistent (conn, acpSession) view under the
// lock. Callers release the lock before doing I/O on the snapshot, so a
// concurrent reconnectIfNeeded / resetACPSession swap does not invalidate
// the call in progress — it only affects the next snapshot.
func (a *ACPAgent) snapshotTransport() (*pkgacp.Conn, pkgacp.SessionID) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.conn, a.acpSession
}

// reconnectIfNeeded checks whether the underlying ACP subprocess is still
// alive. If the process was killed (idle reaper, OOM, crash), it
// transparently creates a new connection and session so the caller's Run()
// succeeds without returning "write |1: file already closed".
//
// Recovery strategy mirrors the cold-start path in NewACPAgent: try to
// LoadSession on the new connection first (preserves subprocess-side
// history without a replay round-trip), and if that fails, fall back to
// NewSession + needReplay so the next Run rebuilds context from
// messages.jsonl. Without the fallback, a single subprocess restart
// where the prior session can no longer be loaded (idle reap that wiped
// session state, OOM mid-write, persisted id not recognised by a newer
// ACP server) would permanently break this agent — every subsequent
// Run would re-enter reconnectIfNeeded, hit the same LoadSession
// failure, and return the same error to the user. Recovery here brings
// it in line with cold-start: conversation continuity is recovered via
// disk replay rather than subprocess-side memory.
func (a *ACPAgent) reconnectIfNeeded(ctx context.Context) error {
	oldConn, oldSession := a.snapshotTransport()
	if oldConn != nil && oldConn.IsAlive() {
		return nil
	}

	// Collect pre-reconnect context BEFORE we drop the dead Conn, so
	// operators can distinguish an idle-reaper close (expected) from an
	// OOM / crash (no reaper line; stderr tail usually holds the reason).
	// ClosedByIdleReap is the canonical signal — it's set by the reaper
	// immediately before Close, so the idle-recycle path takes the INFO
	// branch and keeps the WARN stream reserved for genuine failures.
	var (
		deadPid        int
		stderrTail     string
		closedByReaper bool
	)
	if oldConn != nil {
		deadPid = oldConn.Pid()
		stderrTail = tailStderr(oldConn.Stderr(), 512)
		closedByReaper = oldConn.ClosedByIdleReap()
	}
	if closedByReaper {
		logger.Infof(ctx, "[acp] reconnecting after idle reap: type=%s workdir=%s acpSession=%s deadPid=%d",
			a.agentType, a.workdir, oldSession, deadPid)
	} else {
		logger.Warnf(ctx, "[acp] subprocess dead, reconnecting: type=%s workdir=%s acpSession=%s deadPid=%d stderrTail=%q",
			a.agentType, a.workdir, oldSession, deadPid, stderrTail)
	}

	conn, err := pkgacp.NewTrackedConn(ctx, a.agentType, a.workdir)
	if err != nil {
		return fmt.Errorf("reconnect: get connection failed: %w", err)
	}

	var (
		newSessionID         pkgacp.SessionID
		freshSession         bool
		freshFingerprint     repository.MessagesFingerprint
		thoughtLevelConfigID string
	)
	if oldSession != "" {
		// Prefer session/resume when the agent supports it: unlike
		// session/load, resume restores the subprocess-side context
		// WITHOUT replaying conversation history via session/update.
		// Load-time replay events are structurally identical to freshly
		// generated output (the protocol carries no isReplay flag), so a
		// LoadSession reconnect re-streams every prior turn into this
		// Run's stream handler, which re-persists and re-pushes them —
		// the duplicate-message bug. Resume sidesteps that entirely.
		if conn.SupportsResume() {
			sessResp, resumeErr := conn.ResumeSession(ctx, string(oldSession), a.workdir)
			if resumeErr == nil {
				newSessionID = pkgacp.SessionID(sessResp.SessionID)
				thoughtLevelConfigID = thoughtLevelConfigIDFromSession(sessResp)
				logger.Infof(ctx, "[acp] reconnected via ResumeSession (no replay): acpSession=%s newPid=%d", sessResp.SessionID, conn.Pid())
			} else {
				// Resume was advertised but failed (subprocess lost the
				// session, transient error, etc.). Fall through to
				// LoadSession below rather than straight to fresh+replay:
				// LoadSession can still restore subprocess-side context
				// from a persisted session, preserving continuity.
				logger.Warnf(ctx, "[acp] ResumeSession failed, falling back to LoadSession: oldAcpSession=%s err=%v", oldSession, resumeErr)
			}
		}

		if newSessionID == "" {
			sessResp, loadErr := conn.LoadSession(ctx, string(oldSession), a.workdir)
			if loadErr == nil {
				newSessionID = pkgacp.SessionID(sessResp.SessionID)
				thoughtLevelConfigID = thoughtLevelConfigIDFromSession(sessResp)
				logger.Infof(ctx, "[acp] reconnected via LoadSession: acpSession=%s newPid=%d", sessResp.SessionID, conn.Pid())
			} else {
				// Old subprocess session is no longer loadable (idle reap
				// wiped it, server schema changed, etc.). Fall through to
				// NewSession + replay so the user's conversation continues
				// rather than failing every subsequent Run with the same
				// error. logged at WARN because the reload was expected to
				// work; INFO would hide it from operators investigating
				// "why did this conversation reset".
				logger.Warnf(ctx, "[acp] reconnect LoadSession failed, falling back to fresh session + replay: oldAcpSession=%s err=%v", oldSession, loadErr)
			}
		}
	}

	if newSessionID == "" {
		// Fresh-session fallback: compute fingerprint BEFORE NewSession
		// so the persisted sync baseline matches the disk state the new
		// session was minted against. Fingerprint compute can fail on
		// disk I/O issues; better to abort here than persist a
		// fabricated zero, which would suppress every subsequent drift
		// check.
		fp, fpErr := a.ctxManager.MessagesFingerprint(ctx)
		if fpErr != nil {
			conn.Close()
			return fmt.Errorf("reconnect: messages fingerprint for fresh session failed: %w", fpErr)
		}
		freshFingerprint = fp

		sessResp, newErr := conn.NewSession(ctx, a.workdir)
		if newErr != nil {
			conn.Close()
			return fmt.Errorf("reconnect: create fresh session failed: %w", newErr)
		}
		newSessionID = pkgacp.SessionID(sessResp.SessionID)
		thoughtLevelConfigID = thoughtLevelConfigIDFromSession(sessResp)
		freshSession = true

		// Persist (id, fingerprint) atomically so a restart between here
		// and the next Run sees a consistent pair. Failing to persist
		// is fatal: if disk still holds the OLD id but memory has the
		// NEW one, the next restart would route to LoadSession on the
		// stale id and either fail or silently diverge.
		if a.sessionStore != nil && a.sessionID != "" {
			if saveErr := savePersistedACPState(ctx, a.sessionStore, a.sessionID, sessResp.SessionID, fp); saveErr != nil {
				conn.Close()
				return fmt.Errorf("reconnect: persist fresh acp session state failed: %w", saveErr)
			}
		}
		logger.Infof(ctx, "[acp] reconnected via fresh session + replay: acpSession=%s newPid=%d syncedCount=%d syncedHash=%s", sessResp.SessionID, conn.Pid(), fp.Count, fp.Hash)
	}

	a.mu.Lock()
	a.conn = conn
	a.acpSession = newSessionID
	a.currentModelID = ""
	a.currentMode = ""
	a.currentThoughtLevel = ""
	a.thoughtLevelConfigID = thoughtLevelConfigID
	a.mu.Unlock()

	if freshSession {
		a.storeFingerprint(freshFingerprint)
		// Replay only when there is prior history to inject. An empty
		// disk means the fresh session can run as a brand-new
		// conversation — no replay prefix needed.
		if freshFingerprint.Count > 0 {
			a.needReplay.Store(true)
		}
	}
	return nil
}

// tailStderr returns the last n bytes of s with leading/trailing whitespace
// trimmed, preserving newlines in the middle so multi-line crash traces stay
// readable in the log. A "...(truncated)" marker is prepended when cut.
func tailStderr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "...(truncated)" + s[len(s)-n:]
}

// resetACPSession drops the current subprocess session and creates a fresh
// one. Invoked when the on-disk messages.jsonl diverges from the
// subprocess's view — either because BeginRun rewrote the file to drop an
// orphan tail, or because another path (eino Run, summary compression,
// external edit) wrote to disk between two ACP Runs. In both cases,
// continuing to prompt the old acpSession would make the model see a tail
// that doesn't match disk. The new session starts empty, so `needReplay`
// is set: the next Run loads messages.jsonl and prepends it as a text
// conversation-history block before the user's prompt, restoring context
// continuity. Without the replay the model would answer as if the
// conversation had just started, silently discarding every prior turn on
// disk.
//
// Failure to persist the new id is surfaced as an error so the caller
// does not continue prompting a subprocess session whose id is not
// reflected on disk — on next agent reload, loadPersistedACPState
// would return the stale id and LoadSession would silently diverge
// again.
func (a *ACPAgent) resetACPSession(ctx context.Context) error {
	conn, oldSession := a.snapshotTransport()
	if conn == nil {
		return fmt.Errorf("reset acp session: no active conn")
	}

	// Count BEFORE NewSession: the sync point for the fresh session is
	// the current disk snapshot, and the subprocess has seen nothing
	// yet, so reading the fingerprint first keeps the sync-point
	// semantics colocated with the reset. Doing it before NewSession
	// also means a fingerprint I/O error aborts cleanly without minting
	// a new subprocess session that nothing references — see the
	// orphan cleanup in the savePersistedACPState branch below for why
	// that matters. Fingerprint failure aborts the reset: persisting
	// the zero fingerprint on an I/O error would make the next drift
	// check see "real fingerprint != zero" and reset again, turning a
	// transient error into an infinite reset loop. Mirrors the
	// fresh-session ordering in reconnectIfNeeded.
	fp, err := a.ctxManager.MessagesFingerprint(ctx)
	if err != nil {
		return fmt.Errorf("compute messages fingerprint for sync point failed: %w", err)
	}

	sessResp, err := conn.NewSession(ctx, a.workdir)
	if err != nil {
		return fmt.Errorf("new subprocess session: %w", err)
	}
	newSession := pkgacp.SessionID(sessResp.SessionID)
	thoughtLevelConfigID := thoughtLevelConfigIDFromSession(sessResp)

	if a.sessionStore != nil && a.sessionID != "" {
		if err := savePersistedACPState(ctx, a.sessionStore, a.sessionID, sessResp.SessionID, fp); err != nil {
			// Persist failed: drop the freshly-minted subprocess
			// session before returning so the conn does not retain a
			// session that this agent will never prompt or persist.
			// Without this cleanup, each failed reset leaks an ACP
			// subprocess session (handler registry slot + cancel
			// pointer) and, paired with the in-memory state on
			// session.Service now being persist-then-commit, leaves
			// no stranded references on either side.
			conn.RemoveSession(newSession)
			return fmt.Errorf("persist new acp session id: %w", err)
		}
	}

	// Drop the old subprocess session slot so the conn does not hold
	// onto stream handlers / cancel pointers for a session that will
	// never be prompted again.
	if oldSession != "" {
		conn.RemoveSession(oldSession)
	}

	a.mu.Lock()
	a.acpSession = newSession
	a.currentModelID = ""
	a.currentMode = ""
	a.currentThoughtLevel = ""
	a.thoughtLevelConfigID = thoughtLevelConfigID
	a.mu.Unlock()
	a.storeFingerprint(fp)
	// Fresh subprocess holds no prior turns; the next Run must replay
	// messages.jsonl as a text context block before the user prompt.
	// Set this after the lock so snapshotTransport readers never see a
	// "new session without replay" intermediate state.
	a.needReplay.Store(true)
	logger.Infof(ctx, "[acp] reset subprocess session: sessionId=%s old=%s new=%s syncedCount=%d syncedHash=%s", a.sessionID, oldSession, newSession, fp.Count, fp.Hash)
	return nil
}

// Run sends user messages to the ACP agent and streams events via handler.
// jobID is threaded into the round builder's log label so WARN lines
// ([round] eager flush superseded / drop terminal ...) can be traced back
// to the concrete loop job, not just the session pair.
func (a *ACPAgent) Run(ctx context.Context, userMessages []*schema.Message, handler agui.EventHandler, jobID string) (retErr error) {
	a.running.Add(1)
	defer a.running.Add(-1)

	// Cancel any in-flight Run on this Agent BEFORE acquiring runSem — the
	// old Run holds the slot, and we need it to drop into its deferred
	// cleanup path so it can release the slot. snapshotTransport() reads
	// conn / acpSession off the still-active old Run so the subprocess
	// Cancel notification lands on the right session.
	//
	// Order matters: cancel the local runCtx FIRST so the previous Run can
	// start unwinding immediately, then fire-and-forget the subprocess
	// cancel notification. The notification is best-effort and can stall
	// up to cancelACPSessionTimeout (2s) on a slow stdio peer; running it
	// synchronously before prevCancel() would delay the new Run by that
	// much for no benefit, since the local cancel is what releases the
	// runSem slot we are about to acquire.
	a.mu.Lock()
	prevCancel := a.cancel
	a.mu.Unlock()
	if prevCancel != nil {
		prevConn, prevSession := a.snapshotTransport()
		prevCancel()
		runSwitchLabel := fmt.Sprintf("run-switch sessionId=%s", a.sessionID)
		safe.Go(context.Background(), func() {
			cancelACPSubprocessSession(context.Background(), prevConn, prevSession, runSwitchLabel)
		})
	}

	// Serialise Runs on this Agent. The round.Builder and the conn-level
	// stream handler are shared, so a new Run must wait for the old Run's
	// deferred cleanup (EmitPendingEnds / CollectMessages / handler clear)
	// to finish before resetting the builder and reinstalling the handler.
	// Acquire is ctx-aware: if the previous Run's detached-context cleanup
	// hangs on slow I/O, this Run unblocks on its own ctx cancellation
	// rather than waiting forever.
	select {
	case a.runSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-a.runSem }()

	// Publish this Run's cancel BEFORE any external I/O — reconnectIfNeeded,
	// UpdateACPModelID and UpdateACPMode all talk to the subprocess, and we
	// want Stop / a subsequent Run's cancel-old-run dance to be able to
	// interrupt them via a.cancel. Registering after the I/O used to leave
	// a window where the agent looked uncancellable from outside.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.mu.Lock()
	a.cancel = cancel
	a.cancelGen++
	gen := a.cancelGen
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		if a.cancelGen == gen {
			a.cancel = nil
		}
		a.mu.Unlock()
	}()

	if err := a.reconnectIfNeeded(runCtx); err != nil {
		return err
	}

	// If the previous Run was cancelled and tainted the subprocess session,
	// create a fresh session before prompting. This avoids the scenario where
	// the subprocess returns an immediate empty response on a post-cancel
	// session. Must run after reconnectIfNeeded (needs a live conn) and
	// before snapshotTransport (resetACPSession updates a.acpSession).
	if a.needSessionReset.CompareAndSwap(true, false) {
		logger.Infof(runCtx, "[acp] resetting session after prior cancel: sessionId=%s", a.sessionID)
		if err := a.resetACPSession(runCtx); err != nil {
			a.needSessionReset.Store(true) // restore so next Run retries reset instead of hitting the dirty session
			return fmt.Errorf("reset session after cancel: %w", err)
		}
	}

	conn, acpSession := a.snapshotTransport()
	if conn == nil || !conn.TryAcquireUse() {
		if err := a.reconnectIfNeeded(runCtx); err != nil {
			return err
		}
		conn, acpSession = a.snapshotTransport()
		if conn == nil || !conn.TryAcquireUse() {
			return fmt.Errorf("acp connection unavailable")
		}
	}
	defer conn.ReleaseUse()

	prompt := extractTextFromMessages(userMessages)
	if prompt == "" {
		// Asymmetry guard with the eino path: hasEffectiveUserInput in
		// services/agent/eino/runner.go admits image/audio/video/file-only
		// turns as valid input, but ACP backends today only consume the
		// flattened text prompt produced by extractTextFromMessages. A bare
		// "empty prompt" error there is misleading — the user did supply
		// content, it just isn't a shape this agent can forward. Return a
		// targeted error so the UI / logs make the capability gap obvious
		// instead of looking like an empty-message bug.
		if userMessagesHaveAttachment(userMessages) {
			return fmt.Errorf("acp agent only supports text input; image / audio / video / file attachments are not yet supported")
		}
		return fmt.Errorf("empty prompt")
	}

	// Cross-path drift check: if messages.jsonl has grown (or shrunk)
	// since this agent last flushed a Run, another path wrote to disk in
	// between. The subprocess's internal view of the conversation no
	// longer matches disk, so reset to a fresh subprocess session before
	// prompting. Done here rather than in NewACPAgent because the ACP
	// service caches one ACPAgent per session (services/agent/acp/manager.go):
	// subsequent Runs on the same session reuse the cached instance and
	// never re-enter NewACPAgent — only a per-Run check catches drift
	// introduced by an intervening eino Run. Fingerprint failure aborts
	// the Run rather than proceeding on a fabricated zero, which could
	// either miss real drift (false "no change" → stale subprocess
	// prompts) or force a spurious reset on every Run until the I/O
	// issue clears.
	//
	// Hash mismatch with same count catches the cases CountMessage
	// alone missed: ReplacePlaceholderToolResult rewrites a row in
	// place, hand edits that swap content for equal-length content,
	// summary rewrites that keep the row count stable. Without the
	// hash check those mutations would leave the subprocess prompting
	// against a stale view and silently diverge from disk.
	currentFingerprint, err := a.ctxManager.MessagesFingerprint(runCtx)
	if err != nil {
		return fmt.Errorf("compute messages fingerprint for drift check failed: %w", err)
	}
	syncedFingerprint := a.loadFingerprint()
	if !currentFingerprint.Equal(syncedFingerprint) {
		logger.Warnf(runCtx, "[acp] cross-path drift detected before Run: current=(count=%d hash=%s) synced=(count=%d hash=%s) acpSession=%s — resetting subprocess session",
			currentFingerprint.Count, currentFingerprint.Hash,
			syncedFingerprint.Count, syncedFingerprint.Hash,
			acpSession)
		if err := a.resetACPSession(runCtx); err != nil {
			return fmt.Errorf("reset acp session on drift failed: %w", err)
		}
	}

	truncated, err := a.ctxManager.BeginRun(runCtx, userMessages...)
	if err != nil {
		return fmt.Errorf("persist user messages failed: %w", err)
	}
	if truncated {
		// BeginRun rewrote messages.jsonl to drop an orphan tail. The ACP
		// subprocess's own session state still reflects the pre-truncate
		// history, so continuing to prompt a.acpSession would feed the
		// model a tail that no longer exists on disk. Discard the old
		// subprocess session and create a fresh one so disk and
		// subprocess view realign. resetACPSession sets needReplay so
		// the prior history is injected as a text context block below.
		if err := a.resetACPSession(runCtx); err != nil {
			return fmt.Errorf("reset acp session after truncate failed: %w", err)
		}
	}

	// History replay: when the subprocess session was freshly minted
	// (NewACPAgent cold-start / drift / truncate / reconnect), it holds
	// no conversation memory, yet messages.jsonl may already carry prior
	// turns from this or another agent path. Prepend that history as a
	// text conversation-history block so the model sees the same context
	// it would have seen on a non-reset Run — without the replay, the
	// reply is generated as if the conversation had just started and
	// every prior disk turn is silently ignored. Loading AFTER BeginRun
	// is deliberate: the drift/truncate resets may have rewritten the
	// tail, and we want the replay to reflect the post-truncate state.
	// Trim the just-appended user turn from the loaded tail so the
	// replay block holds only prior context.
	if a.needReplay.Load() {
		historyMsgs, err := a.ctxManager.LoadMessagesForLLM(runCtx)
		if err != nil {
			return fmt.Errorf("load history for replay failed: %w", err)
		}
		if trimmed := trimTrailingUserMessages(historyMsgs, len(userMessages)); len(trimmed) > 0 {
			prefix := buildReplayPrompt(trimmed)
			prompt = prefix + prompt
			logger.Infof(runCtx, "[acp] replaying history: acpSession=%s historyMsgs=%d prefixBytes=%d", acpSession, len(trimmed), len(prefix))
		}
	}

	logger.Debugf(runCtx, "[acp] prompt len=%d acpSession=%s", len(prompt), acpSession)

	// Re-snapshot transport after the possible drift/truncate resets above:
	// resetACPSession publishes a new acpSession, so downstream I/O
	// (AcquirePromptSlot, SetStreamHandler, builder log label, handler
	// cleanup) must run against the current pair, not the pre-reset one.
	conn, acpSession = a.snapshotTransport()

	// Apply the persisted model/mode/thought_level selection on the
	// freshly-snapshotted transport. Held until after the drift /
	// BeginRun-truncate resets so a soon-to-be-discarded subprocess session
	// does not pay for an up-front SetSessionModel / SetSessionMode RPC:
	// reconnectIfNeeded and resetACPSession both clear currentModelID /
	// currentMode, so the fresh-session path lands here with empty cache and
	// fires the RPC against the new session; the no-reset path keeps the
	// previous Run's values cached and short-circuits the RPC when the
	// selection has not changed. Values come from the persisted session (see
	// applyPersistedConfig) rather than Run parameters, so a live switch made
	// between Runs — and the last selection after reconnect / reset — is
	// honoured. Use runCtx so a Stop in flight cancels the RPC rather than
	// blocking on the caller's parent ctx.
	if err := a.applyPersistedConfig(runCtx); err != nil {
		return err
	}

	// Wire the round builder for this Run: reset state, install the agui
	// handler, and set onFlush so completed rounds are persisted immediately
	// rather than only at the end of the turn. onStitch is paired with
	// onFlush so a late tool terminal arriving AFTER an eager superseded
	// flush can replace the [placeholder] superseded row with the real
	// result (in-memory AND on disk). Without it, ACP backends that
	// interleave assistant chunks with still-pending tool terminals would
	// permanently lose the real tool output in history and future LLM
	// context.
	//
	// Both callbacks wrap the caller's ctx in round.PersistContext:
	// detached from caller cancellation (so a flush firing during
	// deferred cleanup after runCtx is cancelled still lands on disk)
	// and tagged with round.PersistTimeout so the ctx-aware repo /
	// chatctx layer observes the canonical deadline (lock-wait phase
	// is bounded; underlying file I/O is not — see PersistTimeout).
	// logCtx keeps the caller's trace / log attrs for breadcrumb
	// logging.
	//
	// persistErr captures the first incremental persistence or
	// placeholder-stitch failure observed during this Run. Streaming keeps
	// going so the UI sees the rest of the response, but the Run returns
	// this error after cleanup so the job runner can mark the iteration as
	// failed — without this, messages.jsonl could silently miss a round
	// (breaking the next round's LLM context, history reload, and ACP drift
	// detection) while the job is reported as completed.
	ctxMgr := a.ctxManager
	logCtx := context.WithoutCancel(ctx)
	var persistErr round.PersistErr

	// Local token-usage fallback. The authoritative source is the
	// subprocess's usage_update (forwarded via builder.OnTokenUsage, which
	// flips SawTokenUsage). But some ACP backends only report usage at turn
	// end — or never — so on a long single round the UI counter would freeze
	// for the whole turn. When the subprocess has not reported usage, reload
	// the on-disk history and tokenise it so the UI still gets a live (if
	// estimated) count. Debounced via tokenUsageMinInterval, with a forced
	// recompute at Run end (see the deferred close-out below). Mirrors the
	// eino path (services/agent/eino/runner.go).
	var (
		lastTokenUsageAt time.Time
		lastTokenUsageMu sync.Mutex
	)
	recomputeTokenUsage := func(force bool) {
		// Prefer the subprocess's authoritative usage_update when present;
		// only estimate locally while the backend stays silent.
		if a.builder.SawTokenUsage() {
			return
		}
		lastTokenUsageMu.Lock()
		if !force && time.Since(lastTokenUsageAt) < tokenUsageMinInterval {
			lastTokenUsageMu.Unlock()
			return
		}
		lastTokenUsageAt = time.Now()
		lastTokenUsageMu.Unlock()

		reloadCtx, reloadCancel := round.PersistContext(ctx)
		defer reloadCancel()
		reloaded, err := ctxMgr.LoadMessagesForLLM(reloadCtx)
		if err != nil {
			// Best-effort: a reload may race a concurrent truncation, so
			// skip and rely on the forced recompute at Run end. Debug so a
			// persistently broken messages.jsonl still leaves a breadcrumb.
			logger.Debugf(logCtx, "[acp] recompute token usage skipped: sessionId=%s err=%v", a.sessionID, err)
			return
		}
		if hErr := handler.OnTokenUsage(tokenizer.MessagesTokenCounter(logCtx, reloaded)); hErr != nil {
			logger.Debugf(logCtx, "[acp] handler OnTokenUsage failed: sessionId=%s err=%v", a.sessionID, hErr)
		}
	}

	a.builder.SetLogLabel(fmt.Sprintf("acp jobId=%s sessionId=%s acpSession=%s", jobID, a.sessionID, acpSession))
	a.builder.Reset(handler, func(msgs []*schema.Message) {
		if len(msgs) == 0 {
			return
		}
		persistCtx, cancel := round.PersistContext(ctx)
		defer cancel()
		if err := ctxMgr.AppendMessages(persistCtx, msgs...); err != nil {
			logger.Warnf(logCtx, "[acp] incremental persist failed: acpSession=%s err=%v", acpSession, err)
			persistErr.Record(fmt.Errorf("acp incremental persist: %w", err))
			// Gate the usage recompute on persistence success so the UI
			// count never gets ahead of on-disk state.
			return
		}
		recomputeTokenUsage(false)
	})
	a.builder.SetStitcher(func(toolCallID string, real *schema.Message) {
		// Error logging happens inside ReplacePlaceholderToolResult; we
		// still record it so a stitch failure surfaces as a Run error
		// rather than only living in logs.
		persistCtx, cancel := round.PersistContext(ctx)
		defer cancel()
		if _, err := ctxMgr.ReplacePlaceholderToolResult(persistCtx, toolCallID, real); err != nil {
			persistErr.Record(fmt.Errorf("acp placeholder stitch: %w", err))
		}
	})
	defer a.builder.ClearOnFlush()

	// Surface the first persistence failure into retErr AFTER the final
	// CollectMessages flush has run. Defers are LIFO, so this defer
	// (declared BEFORE the FinalizeRound defer below) executes AFTER
	// CollectMessages drains the final round through onFlush, ensuring we
	// observe any failure from that final write. PersistErr.CapturePersistErrTo
	// never overwrites an existing retErr (prompt error / cancel takes
	// precedence) — a persist failure on top of a prompt error is noise.
	defer persistErr.CapturePersistErrTo(&retErr, "acp stream completed but persist failed")

	// Acquire the prompt slot FIRST — this cancels any in-flight prompt on
	// this connection and blocks until it actually releases the slot. Only
	// AFTER the slot is ours do we register our builder as the session's
	// stream handler, so residue updates from the cancelled prior prompt
	// cannot reach our fresh round's accumulators. Without this split, a
	// new Run would install its handler via SetStreamHandler before Prompt
	// even got to CancelActivePrompt — any update arriving during that
	// window (old prompt's tail chunks / late tool terminals) would be
	// dispatched to the new builder and corrupt its first round.
	slot, err := conn.AcquirePromptSlot(runCtx, acpSession)
	if err != nil {
		return err
	}
	defer slot.Release()

	handlerGen := conn.SetStreamHandler(acpSession, a.builder)
	defer conn.ClearStreamHandlerIfGen(acpSession, handlerGen)

	// Round close-out runs as a deferred closure so it fires on every
	// exit path — normal return, prompt error, or runCtx cancellation.
	// Declared AFTER ClearOnFlush so LIFO order executes this first,
	// while onFlush is still installed: the final half-complete round
	// reaches disk via the callback rather than being dropped. Mirrors
	// the eino path's defer in services/agent/eino/runner.go so both
	// agent types emit OnMessageEnd / OnThoughtEnd / OnToolCallEnd on
	// cancel — without this, the UI stays stuck on "streaming" state
	// after the user hits stop. Reason depends on why the loop exited:
	//   - runCtx cancelled → ReasonCanceled (user interrupt / shutdown)
	//   - otherwise        → ReasonInterrupted (prompt error or late
	//                        missing terminal on the happy path).
	//
	// EmitPendingEnds MUST run before CollectMessages so UI end events
	// reach the handler before the tool-call accumulators are cleared.
	// Both calls share the same reason so the live Placeholder tooltip
	// matches the on-disk placeholder the next reload will render.
	defer func() {
		reason := round.ReasonInterrupted
		if runCtx.Err() != nil {
			reason = round.ReasonCanceled
			// Notify the subprocess to cancel the current turn. Without
			// this, the subprocess keeps generating after the Go side stops
			// listening. The next Run would send a new prompt into a still-
			// busy subprocess, causing an empty/corrupt response. This
			// closes the gap where neither the "prevCancel" dance (old Run
			// already exited → a.cancel is nil) nor CancelActivePrompt
			// (slot.Release cleared activeSessionID) can fire the cancel.
			// Synchronous: bounded by cancelACPSessionTimeout (2s), ensures
			// the subprocess receives the cancel before slot.Release().
			cancelConn, cancelSession := a.snapshotTransport()
			logger.Infof(context.Background(), "[acp] run context cancelled, notifying subprocess: sessionId=%s acpSession=%s err=%v", a.sessionID, cancelSession, runCtx.Err())
			cancelACPSubprocessSession(context.Background(), cancelConn, cancelSession, "run-context-cancel")
			// Mark the session for reset: some ACP backends leave the
			// session in a tainted state after SessionCancel — the next
			// Prompt returns immediately with no content. The next Run
			// checks this flag and creates a fresh subprocess session.
			a.needSessionReset.Store(true)
		}
		a.builder.EmitPendingEnds(reason)
		a.builder.CollectMessages(reason)

		// Forced final token-usage recompute. Placed after CollectMessages
		// so the final round is already on disk when we tally. recomputeTokenUsage
		// is a no-op when the subprocess reported usage (SawTokenUsage) and
		// otherwise reloads + tokenises the on-disk history, bypassing the
		// debounce so the UI always sees the exact post-Run value even if the
		// last per-flush recompute hit the throttle window.
		recomputeTokenUsage(true)

		// Record the post-Run sync point. After CollectMessages drains the
		// builder via onFlush, messages.jsonl contains exactly what the
		// subprocess session just emitted — this is our "we are in sync"
		// marker. Persist failure is logged, not propagated: the in-memory
		// field keeps the process correct until restart, and a restart-
		// with-stale-persisted-count will just trigger an extra (harmless)
		// reset next time. Wrapped in PersistContext for the same reason
		// as the token fallback above.
		baselineCtx, baselineCancel := round.PersistContext(ctx)
		a.updateSyncBaseline(baselineCtx, persistErr.Err())
		baselineCancel()

		// Drop the agui handler reference now that the post-Run sync
		// baseline is recorded. The Builder is owned by the cached agent
		// but the handler (SSE / job-runner closure) is per-Run; leaving
		// it installed between Runs keeps the closure alive until the
		// next Reset and would route any late callback to a stale
		// handler. Mirrors the eino path's defer in runner.go.
		a.builder.ClearHandler()
	}()

	// Send the prompt on the held slot. Serialization across sessions on
	// this subprocess and active-session tracking are the slot's job; we
	// just send the text and wait for the turn to finish.
	if err := slot.SendPrompt(runCtx, prompt); err != nil {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		// Surface actionable hints for common auth failures so operators
		// don't need to parse the raw RPC error to know what to do.
		errStr := err.Error()
		if strings.Contains(errStr, "refresh token") || strings.Contains(errStr, "access token") || strings.Contains(errStr, "sign in again") {
			logger.Errorf(runCtx, "[acp] auth token expired or revoked — please re-login to the ACP provider (e.g. run the provider's auth flow again): %v", err)
		}
		// Known upstream defect: the ACP backend (e.g. claude-agent-acp)
		// occasionally builds an invalid tool_use sequence — typically right
		// after an image Read tool_result — and the Claude API rejects the
		// follow-up request with "tool use concurrency issues" /
		// tool_use_mismatch (errorKind=invalid_request). See
		// docs/feature/feature-2026-06-07-debug-acp-tool-use-mismatch.md: the bad sequence lives inside
		// the subprocess's own transcript, so Quartet can neither prevent it
		// nor retry past it (resending the same history reproduces the same
		// 400, and rebuilding the session would discard conversation context).
		// Return an actionable message instead of the bare RPC error so the
		// user knows it is an upstream issue and how to get unstuck.
		if strings.Contains(errStr, "tool use concurrency issues") || strings.Contains(errStr, "tool_use_mismatch") {
			logger.Errorf(runCtx, "[acp] upstream tool-use sequence rejected by Claude API (known claude-agent-acp defect, often after an image Read): type=%s acpSession=%s err=%v",
				a.agentType, acpSession, err)
			return fmt.Errorf("ACP backend (%s) produced an invalid tool-use sequence that the Claude API rejected — this is a known upstream issue, often triggered after reading an image. Retrying the same message will fail the same way; start a new conversation to continue. Raw error: %w", a.agentType, err)
		}
		// A "connection closed" / EOF here means the subprocess died mid-prompt
		// — the RPC error alone ("EOF") hides WHY. The real reason (Node crash,
		// OOM, model-side fatal) lands in the subprocess stderr, which the Conn
		// captures. Surface its tail so the failure is diagnosable from logs
		// instead of just "connection closed: EOF". Mirrors the stderr-tail
		// treatment the reconnect path already gives a dead subprocess.
		if pkgacp.IsBenignCloseErr(err) {
			if stderrTail := tailStderr(conn.Stderr(), 2048); stderrTail != "" {
				logger.Errorf(runCtx, "[acp] prompt failed because subprocess closed the connection: type=%s acpSession=%s pid=%d err=%v subprocessStderr=%q",
					a.agentType, acpSession, conn.Pid(), err, stderrTail)
				return fmt.Errorf("acp prompt failed: %w (subprocess stderr: %s)", err, stderrTail)
			}
			logger.Errorf(runCtx, "[acp] prompt failed because subprocess closed the connection (no stderr captured): type=%s acpSession=%s pid=%d err=%v",
				a.agentType, acpSession, conn.Pid(), err)
		}
		return fmt.Errorf("acp prompt failed: %w", err)
	}
	// Prompt delivered successfully — the subprocess has now seen
	// whatever conversation-history prefix we built, so subsequent Runs
	// on this session can rely on its internal memory again. Leaving
	// the flag set on SendPrompt failure is deliberate so a retry
	// injects the same replay block the failed attempt would have.
	a.needReplay.Store(false)

	// Diagnostic: detect empty responses early. If the subprocess returned
	// successfully but the builder received zero stream events, the session
	// is likely in a broken state. Log at WARN so operators notice without
	// needing DEBUG, and so the next occurrence of "run completed with no
	// output" can be traced directly.
	if !a.builder.HasAccumulatedContent() {
		logger.Warnf(runCtx, "[acp] prompt returned successfully but builder has no content: sessionId=%s acpSession=%s — subprocess may have returned empty response", a.sessionID, acpSession)
	}

	if runCtx.Err() != nil {
		return runCtx.Err()
	}
	return nil
}

// stopAndFlushTimeout caps how long StopAndFlush will wait for Run's
// deferred cleanup to drain. Generous enough to cover Run's deferred
// onFlush + post-Run sync baseline (each bounded by round.PersistTimeout)
// plus a small slack; after this we fall back to an inline flush so the
// builder's accumulators still reach disk.
const stopAndFlushTimeout = 12 * time.Second

// StopAndFlush atomically cancels any in-flight Run and ensures the
// builder's accumulated state reaches disk. Replaces the prior
// FlushPendingMessages -> Cancel idiom, which had a real race window:
// FlushPendingMessages emitted pending-end events and cleared the
// builder's accumulators, but the Run was still active and could push
// new stream events into the just-cleared builder. Late tool terminals
// arriving after the eager superseded flush would never reach disk —
// only the canceled placeholder would. Mirrors the eino path's
// StopAndFlush in services/agent/eino/quartet.go.
//
// New order:
//  1. Cancel runCtx (and notify the ACP subprocess of cancel) so the
//     Run loop exits ASAP.
//  2. Wait (bounded by stopAndFlushTimeout) for Run to finish its
//     deferred cleanup chain — that chain runs round.FinalizeRound
//     while onFlush is still installed, so any in-flight round is
//     persisted atomically with the cancel.
//  3. As a safety net, run FlushPendingMessages. If Run already
//     drained the builder, this is a no-op. If Run hung past the
//     timeout, this gets the builder's accumulators onto disk via the
//     inline persist callback that runs because onFlush has been
//     ClearOnFlush'd.
func (a *ACPAgent) StopAndFlush() {
	a.Cancel()
	a.waitForRunExit(stopAndFlushTimeout)
	a.FlushPendingMessages()
}

// FlushPendingMessages flushes any accumulated in-memory messages from the
// current LLM round to persistent storage. This should be called before
// Cancel() to ensure in-flight messages are not lost. Delegates to
// round.FlushPending so cleanup semantics stay identical to the eino path.
// The persist call is wrapped in round.PersistContext so the canonical
// persist deadline applies — see round.PersistTimeout for the scope of
// what that deadline currently covers (lock-wait yes, blocking file I/O
// no).
func (a *ACPAgent) FlushPendingMessages() {
	_, acpSession := a.snapshotTransport()
	round.FlushPending(a.builder, func(msgs []*schema.Message) error {
		ctx, cancel := round.PersistContext(context.Background())
		defer cancel()
		return a.ctxManager.AppendMessages(ctx, msgs...)
	}, fmt.Sprintf("acp acpSession=%s", acpSession))
}

// cancelACPSessionTimeout caps how long a subprocess-directed Cancel
// notification can block. The notification is a fire-and-forget JSON-RPC
// message, but the underlying stdio transport may still stall if the peer
// is slow to drain its read side; bounding the wait keeps user-visible
// "Stop" / run-switch paths responsive.
const cancelACPSessionTimeout = 2 * time.Second

// cancelACPSubprocessSession sends a Cancel notification to the subprocess
// with a short timeout and logs any failure. Callers use a detached parent
// context (WithoutCancel) so cancelling the caller's ctx does not abort the
// cancel itself — the whole point of this call is to run during cancel /
// switch paths. logLabel is a short tag ("run-switch" / "Cancel") shown in
// the warning so the offender is obvious in logs.
//
// "transport closed" / "connection closed" returns are expected on these
// paths: the conn may already be gone (idle reap, peer exit, prior Close),
// and a fire-and-forget cancel that loses that race is benign. We log
// those at debug — including whether the conn was reaped, so a real
// upstream cancel failure is not buried under teardown noise.
func cancelACPSubprocessSession(ctx context.Context, conn *pkgacp.Conn, acpSession pkgacp.SessionID, logLabel string) {
	if conn == nil || acpSession == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelACPSessionTimeout)
	defer cancel()
	err := conn.CancelSession(ctx, acpSession)
	if err == nil {
		return
	}
	if pkgacp.IsBenignCloseErr(err) {
		logger.Debugf(ctx, "[acp] cancel subprocess session (%s) skipped: acpSession=%s reaped=%v err=%v",
			logLabel, acpSession, conn.ClosedByIdleReap(), err)
		return
	}
	logger.Warnf(ctx, "[acp] cancel subprocess session (%s) failed: acpSession=%s reaped=%v err=%v",
		logLabel, acpSession, conn.ClosedByIdleReap(), err)
}

// Cancel stops the current run.
//
// Order matters: cancel the local runCtx FIRST so the in-flight Run starts
// unwinding immediately, then fire-and-forget the subprocess cancel
// notification. The notification can stall up to cancelACPSessionTimeout
// (2s) on a slow stdio peer; running it synchronously would delay the
// user-visible Stop / StopAndFlush / Close path by that much without
// affecting how fast the local Run actually exits.
func (a *ACPAgent) Cancel() {
	a.mu.RLock()
	cancel := a.cancel
	a.mu.RUnlock()
	if cancel != nil {
		cancel()
	}

	conn, acpSession := a.snapshotTransport()
	logger.Debugf(context.Background(), "[acp] cancel: acpSession=%s", acpSession)
	cancelLabel := fmt.Sprintf("Cancel sessionId=%s", a.sessionID)
	safe.Go(context.Background(), func() {
		cancelACPSubprocessSession(context.Background(), conn, acpSession, cancelLabel)
	})
}

// Close releases the ACPAgent resources.
// The underlying subprocess is managed by the pool and not killed here.
//
// Cancel() unwinds the in-flight Run, but Run's deferred cleanup
// (EmitPendingEnds / CollectMessages / token-usage fallback / post-run
// sync count persist — see services/agent/acp/agent.go:703) still needs
// to finish before we drop the stream handler. Without the wait, a
// concurrent Close from cleanupSessions can race the deferred block:
// late terminal events arrive after RemoveSession and get dropped, the
// final round's Placeholder ends never reach the UI, and the persisted
// sync count baseline can lag the on-disk messages.jsonl. Mirrors the
// eino path's Close (services/agent/eino/quartet.go:308) so both
// agents have the same shutdown ordering.
func (a *ACPAgent) Close() {
	a.Cancel()
	a.waitForRunExit(5 * time.Second)
	conn, acpSession := a.snapshotTransport()
	if conn != nil && acpSession != "" {
		conn.RemoveSession(acpSession)
	}
}

// waitForRunExit polls the running counter until it drops to zero or
// the deadline elapses. Polling is fine here: Run exits quickly once
// runCtx is cancelled, and this path is only hit on shutdown / cleanup.
// Bounded so a stuck deferred block can't block shutdown indefinitely;
// on timeout we proceed anyway and log — the deferred cleanup uses a
// detached context so what it can persist still reaches disk.
func (a *ACPAgent) waitForRunExit(timeout time.Duration) {
	if a.running.Load() == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if a.running.Load() == 0 {
			return
		}
		if time.Now().After(deadline) {
			logger.Warnf(context.Background(), "[acp] Close timed out waiting for Run to exit: sessionId=%s running=%d", a.sessionID, a.running.Load())
			return
		}
		<-tick.C
	}
}

// SessionID returns the subprocess-level ACP session id. Exposed for
// observability (debug logging in external callers); callers MUST treat
// it as read-only — it can change under the hood when resetACPSession /
// reconnectIfNeeded mints a fresh subprocess session.
func (a *ACPAgent) SessionID() pkgacp.SessionID {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.acpSession
}

// IsRunning reports whether a Run() invocation is currently in flight.
// Used by the service layer to skip candidates during LRU eviction.
func (a *ACPAgent) IsRunning() bool {
	return a.running.Load() > 0
}

// UpdateACPModelID pushes a model selection to the subprocess session.
// Returns nil on success or when the selection already matches the
// last-pushed value (no-op). A failed SetSessionModel surfaces as an
// error — model identity is part of the Run's semantic contract, so
// silently continuing on the previously-active model would cause the
// caller to respond with a model the user did not choose. currentModelID
// stays unchanged on failure so a retry (next Run with the same id) will
// re-attempt instead of being short-circuited by the equality check.
func (a *ACPAgent) UpdateACPModelID(ctx context.Context, modelID string) error {
	a.mu.RLock()
	skip := modelID == a.currentModelID
	a.mu.RUnlock()
	if skip {
		return nil
	}
	_, err := a.setModel(ctx, modelID)
	return err
}

// SetModel switches the model on the live session and returns the refreshed
// selector lists carried in the ACP response (model + thought_level may both
// change as a linked side effect). Unlike UpdateACPModelID it does not
// short-circuit on an unchanged value: the caller is an explicit user switch
// that wants the freshly-linked ConfigOptions back.
func (a *ACPAgent) SetModel(ctx context.Context, modelID string) (*model.ACPConfigState, error) {
	resp, err := a.setModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	return acpstate.ConfigState(resp), nil
}

// setModel performs the model RPC and updates the current-model cache,
// returning the session response so callers can surface refreshed
// ConfigOptions. currentModelID stays unchanged on failure so a retry
// re-attempts instead of being short-circuited by an equality check.
func (a *ACPAgent) setModel(ctx context.Context, modelID string) (*pkgacp.SessionResponse, error) {
	a.mu.RLock()
	conn := a.conn
	acpSession := a.acpSession
	old := a.currentModelID
	a.mu.RUnlock()

	if conn == nil {
		return nil, fmt.Errorf("set model: no active conn")
	}
	resp, err := conn.SetSessionModel(ctx, acpSession, modelID)
	if err != nil {
		return nil, fmt.Errorf("set model failed: acpSession=%s model=%s: %w", acpSession, modelID, err)
	}
	// A model switch relinks the session's ConfigOptions: the thought_level
	// selector — and the config id used to drive it — can change or vanish
	// entirely for a model that does not support reasoning effort. Refresh
	// the cached config id from the linked response so a later
	// SetSessionThoughtLevel targets the right option (or is skipped when the
	// new model advertises none), and clear the last-pushed thought_level so
	// applyPersistedConfig re-applies the persisted selection against the
	// newly-linked session instead of short-circuiting on a stale value.
	newThoughtLevelConfigID := thoughtLevelConfigIDFromSession(resp)
	a.mu.Lock()
	a.currentModelID = modelID
	a.thoughtLevelConfigID = newThoughtLevelConfigID
	a.currentThoughtLevel = ""
	a.mu.Unlock()
	logger.Debugf(ctx, "[acp] set model: acpSession=%s model=%s prev=%s thoughtLevelConfigID=%s", acpSession, modelID, old, newThoughtLevelConfigID)
	return resp, nil
}

// UpdateACPMode is the mode counterpart to UpdateACPModelID with the
// same fail-fast contract: a SetSessionMode failure is returned so the
// caller does not run under a mode different from what the user
// requested. Mode affects tool availability and assistant behavior, so
// "log-and-continue" would produce a silent semantic mismatch.
// Exception: antigravity-acp does not implement session/set_mode, so
// failures are logged and skipped for that agent type.
func (a *ACPAgent) UpdateACPMode(ctx context.Context, mode string) error {
	a.mu.RLock()
	skip := mode == a.currentMode
	agentType := a.agentType
	a.mu.RUnlock()
	if skip {
		return nil
	}
	_, err := a.setMode(ctx, mode)
	if err != nil && agentType == "antigravity-acp" {
		logger.Warnf(ctx, "[acp] set mode skipped for antigravity-acp: %v", err)
		return nil
	}
	return err
}

// SetMode switches the mode on the live session. The mode RPC carries no
// ConfigOptions, so the returned state has all lists nil — the caller keeps
// its current selector lists and just reflects the newly-active mode.
func (a *ACPAgent) SetMode(ctx context.Context, mode string) (*model.ACPConfigState, error) {
	resp, err := a.setMode(ctx, mode)
	if err != nil {
		return nil, err
	}
	return acpstate.ConfigState(resp), nil
}

func (a *ACPAgent) setMode(ctx context.Context, mode string) (*pkgacp.SessionResponse, error) {
	a.mu.RLock()
	conn := a.conn
	acpSession := a.acpSession
	a.mu.RUnlock()

	if conn == nil {
		return nil, fmt.Errorf("set mode: no active conn")
	}
	resp, err := conn.SetSessionMode(ctx, acpSession, mode)
	if err != nil {
		return nil, fmt.Errorf("set mode failed: acpSession=%s mode=%s: %w", acpSession, mode, err)
	}
	a.mu.Lock()
	a.currentMode = mode
	a.mu.Unlock()
	logger.Debugf(ctx, "[acp] set mode: acpSession=%s mode=%s", acpSession, mode)
	return resp, nil
}

// UpdateACPThoughtLevel is the thought_level counterpart to UpdateACPMode.
// thought_level has no dedicated RPC, so it is pushed through the generic
// SetSessionConfigOption keyed by the config id discovered from the session
// (e.g. "reasoning_effort"). Same fail-fast contract: a failure is returned
// so the caller does not run under a thought_level different from what the
// user requested. currentThoughtLevel stays unchanged on failure so a retry
// re-attempts instead of being short-circuited by the equality check.
func (a *ACPAgent) UpdateACPThoughtLevel(ctx context.Context, thoughtLevel string) error {
	a.mu.RLock()
	skip := thoughtLevel == a.currentThoughtLevel
	// The active model may not support reasoning effort at all — switching to
	// such a model clears thoughtLevelConfigID (see setModel). Replaying a
	// persisted thought_level here would push an unknown config option and
	// fail the whole Run, so treat "no config id" as "nothing to apply". A
	// genuine live switch (SetThoughtLevel) still surfaces the error via
	// setThoughtLevel; this skip only covers the persisted-replay path.
	noConfigID := a.thoughtLevelConfigID == ""
	acpSession := a.acpSession
	a.mu.RUnlock()
	if skip || noConfigID {
		if noConfigID {
			logger.Debugf(ctx, "[acp] skip thought_level replay: acpSession=%s thoughtLevel=%s (active model advertises no thought_level option)", acpSession, thoughtLevel)
		}
		return nil
	}
	_, err := a.setThoughtLevel(ctx, thoughtLevel)
	return err
}

// SetThoughtLevel switches the thought_level on the live session and returns
// the refreshed selector lists carried in the ACP response (model +
// thought_level may both change as a linked side effect).
func (a *ACPAgent) SetThoughtLevel(ctx context.Context, thoughtLevel string) (*model.ACPConfigState, error) {
	resp, err := a.setThoughtLevel(ctx, thoughtLevel)
	if err != nil {
		return nil, err
	}
	return acpstate.ConfigState(resp), nil
}

func (a *ACPAgent) setThoughtLevel(ctx context.Context, thoughtLevel string) (*pkgacp.SessionResponse, error) {
	a.mu.RLock()
	conn := a.conn
	acpSession := a.acpSession
	configID := a.thoughtLevelConfigID
	a.mu.RUnlock()

	if conn == nil {
		return nil, fmt.Errorf("set thought_level: no active conn")
	}
	if configID == "" {
		return nil, fmt.Errorf("set thought_level failed: acpSession=%s thoughtLevel=%s: agent does not advertise a thought_level config option", acpSession, thoughtLevel)
	}
	resp, err := conn.SetSessionThoughtLevel(ctx, acpSession, configID, thoughtLevel)
	if err != nil {
		return nil, fmt.Errorf("set thought_level failed: acpSession=%s thoughtLevel=%s: %w", acpSession, thoughtLevel, err)
	}
	a.mu.Lock()
	a.currentThoughtLevel = thoughtLevel
	a.mu.Unlock()
	logger.Debugf(ctx, "[acp] set thought_level: acpSession=%s thoughtLevel=%s configID=%s", acpSession, thoughtLevel, configID)
	return resp, nil
}

// applyPersistedConfig pushes the session's persisted model / mode /
// thought_level selection onto the (possibly freshly-minted) subprocess
// session. Called from Run after drift / reconnect resets have settled, so a
// fresh session picks up the user's last selection while a no-reset session
// short-circuits via the Update* equality checks. Values come from the
// persisted session rather than Run parameters: live switches (SetModel etc.)
// persist to the session, and reconnect / reset clear the current* cache, so
// the persisted session is the single source of truth for what the user
// picked.
func (a *ACPAgent) applyPersistedConfig(ctx context.Context) error {
	modelID, acpMode, thoughtLevel := a.persistedConfig()
	if modelID != "" {
		if err := a.UpdateACPModelID(ctx, modelID); err != nil {
			return err
		}
	}
	if acpMode != "" {
		if err := a.UpdateACPMode(ctx, acpMode); err != nil {
			return err
		}
	}
	if thoughtLevel != "" {
		if err := a.UpdateACPThoughtLevel(ctx, thoughtLevel); err != nil {
			return err
		}
	}
	return nil
}

// persistedConfig reads the ACP model / mode / thought_level selection from
// the persisted session. Returns empty strings when the store or session is
// unavailable, in which case applyPersistedConfig leaves the subprocess
// session on its defaults.
func (a *ACPAgent) persistedConfig() (modelID, acpMode, thoughtLevel string) {
	if a.sessionStore == nil || a.sessionID == "" {
		return "", "", ""
	}
	s, ok := a.sessionStore.Get(a.sessionID)
	if !ok {
		return "", "", ""
	}
	return s.ModelID, s.ACPMode, s.ACPThoughtLevel
}

func extractTextFromMessages(messages []*schema.Message) string {
	var parts []string
	for _, m := range messages {
		if m == nil {
			continue
		}
		if m.Content != "" {
			parts = append(parts, m.Content)
			continue
		}
		// Fallback: extract text from UserInputMultiContent (multimodal
		// messages where Content is empty and text lives in the parts).
		for _, part := range m.UserInputMultiContent {
			if part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// userMessagesHaveAttachment reports whether any of the supplied user
// messages carries a non-text part (image / audio / video / file). Used
// alongside extractTextFromMessages so the ACP Run can distinguish an
// "empty user input" turn (genuine bug) from an "attachment-only" turn
// (capability gap with the eino path) and surface the right error.
func userMessagesHaveAttachment(messages []*schema.Message) bool {
	for _, m := range messages {
		if m == nil {
			continue
		}
		for _, part := range m.UserInputMultiContent {
			if part.Image != nil || part.Audio != nil || part.Video != nil || part.File != nil {
				return true
			}
		}
	}
	return false
}

// RequiresRebuild reports whether a cached ACPAgent must be discarded and
// recreated to honour the next call's parameters. agentType / workdir are
// baked into the subprocess + tracked-conn at construction (NewTrackedConn,
// env loading) and cannot be hot-swapped. The model is hot-swapped on the
// live session via SetSessionModel (from applyPersistedConfig at Run time or
// an explicit SetModel switch), so it does not factor into rebuild.
func (a *ACPAgent) RequiresRebuild(agentType, workdir string) bool {
	return a.agentType != agentType || a.workdir != workdir
}
