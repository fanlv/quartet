package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	httpSandbox "github.com/deep-agent/sandbox/sdk/go/http"
	localSandbox "github.com/deep-agent/sandbox/sdk/go/local"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/repository"
	typesmodel "github.com/fanlv/quartet/types/model"
	"golang.org/x/sync/singleflight"
)

// SandboxRefSink is the narrow contract the Manager uses to publish a
// workspace→container binding. It is satisfied by
// services/workspace.Service so the workspace's in-memory copy is
// updated in the same critical section as its on-disk meta.json. Without
// this path, any subsequent service.Update / EnsureDefault / etc. would
// save the stale in-memory workspace (no Sandbox field) and overwrite
// the ref the Manager just wrote — which then breaks container recovery
// after a quartet restart.
//
// pkg/sandbox intentionally does not import services/workspace (that
// would pull a bunch of service-layer deps into a low-level package),
// hence the interface plus a process-wide setter below.
type SandboxRefSink interface {
	SetSandboxRef(workspaceID string, ref *typesmodel.SandboxRef) error
}

var (
	refSinkMu sync.Mutex
	refSink   SandboxRefSink
)

// SetRefSink wires the Manager to a SandboxRefSink (typically the
// workspace service). Called once at process boot, before the first
// sandbox.New(). If unset, Manager.persistRef falls back to writing
// through the repository directly, which is the right behaviour for
// tests and standalone use but skips the in-memory sync.
func SetRefSink(sink SandboxRefSink) {
	refSinkMu.Lock()
	defer refSinkMu.Unlock()
	refSink = sink
}

func currentRefSink() SandboxRefSink {
	refSinkMu.Lock()
	defer refSinkMu.Unlock()
	return refSink
}

// Manager tuning defaults. These are the values from refactor §4.11; they
// can be overridden by env vars (QUARTET_SANDBOX_*) so operators can
// adjust without a rebuild.
const (
	defaultCapacity      = 8
	defaultIdleTimeout   = 20 * time.Minute
	defaultHealthTimeout = 30 * time.Second
	// defaultBringUpTimeout covers `docker compose up -d`, which on a cold
	// host has to pull the sandbox image before the service starts. 30s was
	// enough once the image was already present but too tight for the first
	// bring-up on a fresh machine / after a base-image rev, so we keep
	// health probes snappy (healthTimeout) and give compose up its own,
	// much larger budget.
	defaultBringUpTimeout = 5 * time.Minute
	defaultHealthProbe    = 60 * time.Second
	defaultTemplate       = "default"
)

const (
	envCapacity       = "QUARTET_SANDBOX_CAPACITY"
	envIdleTimeout    = "QUARTET_SANDBOX_IDLE_TIMEOUT"
	envHealthTimeout  = "QUARTET_SANDBOX_HEALTH_TIMEOUT"
	envBringUpTimeout = "QUARTET_SANDBOX_BRINGUP_TIMEOUT"
	envHealthProbe    = "QUARTET_SANDBOX_HEALTH_INTERVAL"
	envKeepOnExit     = "QUARTET_SANDBOX_KEEP_ON_EXIT"
	envSandboxImage   = "QUARTET_SANDBOX_IMAGE"
)

// entry is the per-workspace container record kept in memory. It tracks
// the runtime baseURL (re-discovered from docker; never persisted), ref
// count, and the timestamps the reaper / capacity manager consult.
//
// tearing + tornDone gate the teardown path: once a reaper / capacity
// eviction decides to drop an entry, it flips tearing=true while still
// holding m.mu, leaves the entry in m.entries, then runs the actual
// compose down asynchronously. A concurrent acquire for the same
// workspaceID sees tearing=true, waits on tornDone, and only then
// attempts to bring a fresh container back up. Without this gate,
// teardown + re-acquire races produce a window where compose down
// kills the just-restarted container (they share the same
// projectName and state directory).
type entry struct {
	workspaceID string
	projectName string
	baseURL     string
	// workdir records the host-side bind-mount used when the container
	// was brought up. Kept only so ensureContainer can detect (and warn
	// about) a later acquire that tries to reuse the container with a
	// different workdir — docker's bind mount is fixed at compose-up,
	// so silently ignoring a mismatch would serve the wrong tree.
	workdir   string
	refCount  int
	healthy   bool
	lastUsed  time.Time
	lastProbe time.Time
	tearing   bool
	tornDone  chan struct{}
}

// Manager owns every per-workspace sandbox container in the process. It
// is created once at boot and torn down at shutdown; handlers reach it
// via getManager(). Local-form requests go through acquire() too but
// never touch the container bookkeeping.
type Manager struct {
	mu      sync.Mutex
	entries map[string]*entry
	sf      singleflight.Group
	driver  containerDriver
	// bringUps tracks an in-flight (or about-to-be-flight) compose-up per
	// workspace, together with the number of callers currently waiting on
	// it. When every waiter has cancelled its own ctx, the remaining
	// compose-up is pointless — nobody will ever consume the resulting
	// container before the reaper picks it up as idle. The entry's
	// innerCancel lets the last-departing waiter abort the bring-up
	// instead of letting docker keep pulling images for no one.
	bringUpMu   sync.Mutex
	bringUps    map[string]*bringUpTask
	repoFactory func() (repository.WorkspaceRepo, error)
	// managerCtx is cancelled when Shutdown starts. It is used to abort
	// background bringUp work that was intentionally detached from any single
	// caller's ctx (singleflight semantics).
	managerCtx    context.Context
	managerCancel context.CancelFunc
	capacity      int
	idleTimeout   time.Duration
	healthTO      time.Duration
	bringUpTO     time.Duration
	healthProbe   time.Duration
	keepOnClose   bool
	reaperCancel  context.CancelFunc
	reaperDone    chan struct{}
	// teardownWG tracks async tearDownAndRemove goroutines started by the
	// reaper / capacity eviction path, so Shutdown can wait for them to
	// finish before returning. Without this the process could exit with
	// compose-down still in flight and leak the container state dir.
	teardownWG sync.WaitGroup
	closed     bool
}

var (
	managerMu   sync.Mutex
	managerInst *Manager
)

// getManager returns the process-wide Manager singleton, initialising it
// on first use. Initialisation cannot fail: the docker driver is allowed
// to report "unavailable" lazily so Local-only deployments can still run
// on machines without docker installed.
//
// Previously this used sync.Once, which made Shutdown irreversible within
// a process — calling Shutdown then getManager() returned the same closed
// instance. The mutex-based init below lets Shutdown atomically clear the
// singleton, so a subsequent call re-initialises a fresh Manager. This
// matters for tests and any future hot-reload path.
func getManager() *Manager {
	managerMu.Lock()
	defer managerMu.Unlock()
	if managerInst != nil {
		return managerInst
	}
	managerInst = newManager()
	managerInst.startReaper()
	// Best-effort adoption of surviving containers from a previous
	// run. Never blocks boot: failure just means we re-create on
	// the next acquire, reusing the same compose project name.
	recCtx, recCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer recCancel()
	managerInst.recover(recCtx)
	return managerInst
}

// resetManagerSingleton drops the cached Manager so the next getManager()
// call initialises a fresh instance. Separated from Shutdown so callers
// that want to keep the torn-down Manager reachable (e.g. for post-mortem
// diagnostics) don't pay the reset cost; the package-level Shutdown chains
// both calls together.
func resetManagerSingleton() {
	managerMu.Lock()
	managerInst = nil
	managerMu.Unlock()
}

func newManager() *Manager {
	managerCtx, managerCancel := context.WithCancel(context.Background())
	m := &Manager{
		entries:       make(map[string]*entry),
		bringUps:      make(map[string]*bringUpTask),
		driver:        newComposeDriver(),
		repoFactory:   repository.NewWorkspaceRepo,
		managerCtx:    managerCtx,
		managerCancel: managerCancel,
		capacity:      envInt(envCapacity, defaultCapacity),
		idleTimeout:   envDuration(envIdleTimeout, defaultIdleTimeout),
		healthTO:      envDuration(envHealthTimeout, defaultHealthTimeout),
		bringUpTO:     envDuration(envBringUpTimeout, defaultBringUpTimeout),
		healthProbe:   envDuration(envHealthProbe, defaultHealthProbe),
		keepOnClose:   envBool(envKeepOnExit, false),
	}
	return m
}

// ErrManagerClosed is returned by acquire paths after Shutdown has been
// called. Handlers can match on it to render a "sandbox unavailable"
// response instead of surfacing a generic internal error.
var ErrManagerClosed = errors.New("sandbox.Manager: closed")

// acquire is the single entry point used by sandbox.New. It dispatches on
// RunInSandbox: false returns a local client unconditionally; true either
// re-uses a healthy entry (ref +1) or goes through the singleflight-guarded
// ensure() path to bring a container online.
func (m *Manager) acquire(workspaceID, workdir string, cfg *newConfig) (*Client, error) {
	if !cfg.RunInSandbox {
		return newLocalClient(workdir)
	}
	if workdir == "" {
		return nil, fmt.Errorf("sandbox.New: workdir is required for container form (workspace %s)", workspaceID)
	}
	// Refuse new containers once Shutdown has started. Otherwise a late
	// acquire would bringUp a container that nothing reaps (the reaper
	// has been stopped and m.entries is about to be wiped), leaking the
	// container on process exit.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	m.mu.Unlock()
	ctx := cfg.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ent, err := m.ensureContainer(ctx, workspaceID, workdir)
	if err != nil {
		return nil, err
	}

	client := httpSandbox.NewClient(ent.baseURL,
		httpSandbox.WithTimeout(cfg.RequestTO),
		httpSandbox.WithCwd(containerWorkdir(workspaceID)),
	)

	sandboxCtx, err := client.GetContext()
	if err != nil {
		m.releaseEntry(ent)
		return nil, fmt.Errorf("sandbox.New: get context from container failed: %w", err)
	}

	return &Client{
		Client:  client,
		Ctx:     sandboxCtx,
		Workdir: containerWorkdir(workspaceID),
		release: func() { m.releaseEntry(ent) },
	}, nil
}

// ensureContainer returns an entry whose container is up and healthy. Uses
// singleflight so concurrent first-time callers share one compose-up.
//
// # ctx semantics
//
// singleflight has a footgun: if the first-arriving caller's ctx is
// cancelled mid-bringUp, the returned error (ctx.Canceled /
// DeadlineExceeded) is broadcast to every waiter even though their own
// ctx is still alive. To avoid that, we do two things:
//   - the closure below uses a detached ctx (WithoutCancel) for the
//     actual bringUp and internal waits, so one caller's cancel does not
//     abort the work the other callers are depending on;
//   - each caller's ctx is still honoured at this outer layer via
//     DoChan + select, so a cancelled caller returns promptly instead
//     of blocking on the shared work.
//
// Waiter counting (see bringUps / bringUpTask) closes the obvious hole
// in (1): when *every* waiter has cancelled, the compose-up is no longer
// serving anyone, so the last caller to leave flips innerCancel. The
// resulting container would otherwise land in m.entries with refCount=0
// and sit idle until the reaper reclaimed it one idleTimeout later —
// wasted docker pulls, wasted memory, wasted ports.

// bringUpTask is the per-workspace state shared by every concurrent
// caller currently waiting on a compose-up. It is created lazily under
// bringUpMu when the first caller enters ensureContainer and deleted
// when the singleflight closure returns. `innerCancel` is populated by
// the closure once it has derived innerCtx from managerCtx; if every
// caller cancels before that point, the closure checks waiters == 0 on
// publish and self-cancels immediately.
type bringUpTask struct {
	innerCancel context.CancelFunc
	waiters     int
}

func (m *Manager) ensureContainer(ctx context.Context, workspaceID, workdir string) (*entry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Retry cap guards against a pathological teardown-restart loop where
	// tornDone keeps closing and a fresh entry immediately enters tearing
	// again. Under normal operation we iterate at most 2-3 times (slow
	// path + one tearing wait + one retry); the cap is well above that.
	const maxEnsureAttempts = 16
	for attempt := 0; attempt < maxEnsureAttempts; attempt++ {
		// Fast path: reuse a healthy, non-tearing entry without singleflight so
		// each concurrent caller increments refCount independently.
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, ErrManagerClosed
		}
		if ent, ok := m.entries[workspaceID]; ok {
			if ent.tearing {
				done := ent.tornDone
				m.mu.Unlock()
				select {
				case <-done:
					continue
				case <-time.After(2 * m.healthTO):
					logger.Warn("[sandbox.Manager] ensureContainer: timeout waiting for teardown of workspace=%s; retrying", workspaceID)
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if ent.healthy {
				if workdir != "" && ent.workdir != "" && ent.workdir != workdir {
					logger.Warn("[sandbox.Manager] ensureContainer: workdir mismatch on reuse workspace=%s existing=%s requested=%s (serving existing bind-mount)",
						workspaceID, ent.workdir, workdir)
				}
				ent.refCount++
				ent.lastUsed = time.Now()
				m.mu.Unlock()
				return ent, nil
			}
		}
		m.mu.Unlock()

		// Register this caller as a waiter on the (possibly yet-to-start)
		// bring-up. The counter is decremented in the deferred cleanup
		// below regardless of how we exit the select; when it hits zero
		// with an innerCancel already published, the last-out caller
		// aborts the docker pull / compose up that would otherwise run
		// for no one.
		m.bringUpMu.Lock()
		task, ok := m.bringUps[workspaceID]
		if !ok {
			task = &bringUpTask{}
			m.bringUps[workspaceID] = task
		}
		task.waiters++
		m.bringUpMu.Unlock()
		waiterReleased := false
		releaseWaiter := func() {
			if waiterReleased {
				return
			}
			waiterReleased = true
			m.bringUpMu.Lock()
			task.waiters--
			if task.waiters == 0 && task.innerCancel != nil {
				task.innerCancel()
			}
			m.bringUpMu.Unlock()
		}

		ch := m.sf.DoChan(workspaceID, func() (any, error) {
			// Detach from any single caller's ctx while still aborting when the
			// Manager is shutting down.
			innerCtx, innerCancel := context.WithCancel(context.WithoutCancel(ctx))
			mgrCtx := m.managerCtx
			if mgrCtx == nil {
				mgrCtx = context.Background()
			}
			go func(managerCtx context.Context) {
				select {
				case <-managerCtx.Done():
					innerCancel()
				case <-innerCtx.Done():
				}
			}(mgrCtx)
			// Publish innerCancel so callers departing via ctx.Done can
			// abort us if they're the last one out. If every waiter
			// already left before we got this far, self-cancel so we
			// don't do work no one wants.
			m.bringUpMu.Lock()
			if t, ok := m.bringUps[workspaceID]; ok {
				t.innerCancel = innerCancel
				if t.waiters == 0 {
					innerCancel()
				}
			}
			m.bringUpMu.Unlock()
			defer func() {
				innerCancel()
				m.bringUpMu.Lock()
				// Clear our task so the next caller (which arrives
				// after singleflight forgets this key) creates a fresh
				// one. Singleflight guarantees no other fn is running
				// under this key while we are, so we can't race with a
				// concurrent bring-up here.
				delete(m.bringUps, workspaceID)
				m.bringUpMu.Unlock()
			}()
			for {
				if err := innerCtx.Err(); err != nil {
					return nil, err
				}
				m.mu.Lock()
				if m.closed {
					m.mu.Unlock()
					return nil, ErrManagerClosed
				}
				ent, ok := m.entries[workspaceID]
				if ok && ent.tearing {
					done := ent.tornDone
					m.mu.Unlock()
					select {
					case <-done:
					case <-time.After(2 * m.healthTO):
						logger.Warn("[sandbox.Manager] ensureContainer: timeout waiting for teardown of workspace=%s; retrying", workspaceID)
					case <-innerCtx.Done():
						return nil, innerCtx.Err()
					}
					continue
				}
				if ok && ent.healthy {
					m.mu.Unlock()
					return ent, nil
				}
				m.mu.Unlock()
				break
			}

			// Outside the lock: do I/O (compose up, probe) so concurrent
			// acquires for other workspaces are not blocked.
			ent, err := m.bringUp(innerCtx, workspaceID, workdir)
			if err != nil {
				return nil, err
			}

			m.mu.Lock()
			if m.closed {
				// Raced with Shutdown: tear the freshly brought-up
				// container down rather than leak it.
				m.mu.Unlock()
				m.tearDown(context.Background(), ent)
				return nil, ErrManagerClosed
			}
			defer m.mu.Unlock()
			// Another singleflight waiter might have registered an entry for
			// this workspace while we were probing. In that case we must NOT
			// replace the canonical map entry, because callers may still hold
			// leases (refCount) against the existing *entry pointer.
			//
			// Instead, merge the fresh bringUp result back into the canonical
			// entry (baseURL/health/probe timestamp) and keep refCount intact.
			if existing, ok := m.entries[workspaceID]; ok && !existing.tearing && existing.projectName == ent.projectName {
				if existing.workdir != "" && ent.workdir != "" && existing.workdir != ent.workdir {
					logger.Warn("[sandbox.Manager] ensureContainer: workdir mismatch on bringUp workspace=%s existing=%s brought_up=%s (updating cached workdir to live bind-mount)",
						workspaceID, existing.workdir, ent.workdir)
				}
				mergeEntryState(existing, ent)
				return existing, nil
			}
			// NOTE: refCount is intentionally NOT incremented here. Each caller
			// acquires its own lease via the fast-path at the top of the outer
			// loop so concurrent waiters don't share a single increment.
			ent.refCount = 0
			ent.lastUsed = time.Now()
			m.entries[workspaceID] = ent
			m.evictIfOverCapLocked(workspaceID)
			return ent, nil
		})

		select {
		case <-ctx.Done():
			releaseWaiter()
			return nil, ctx.Err()
		case res := <-ch:
			releaseWaiter()
			if res.Err != nil {
				return nil, res.Err
			}
			// Loop back to acquire the lease (refCount++) under lock.
			continue
		}
	}
	return nil, fmt.Errorf("sandbox.Manager: ensureContainer gave up after %d attempts for workspace=%s", maxEnsureAttempts, workspaceID)
}

func mergeEntryState(existing, fresh *entry) {
	if existing == nil || fresh == nil {
		return
	}
	existing.baseURL = fresh.baseURL
	existing.workdir = fresh.workdir
	existing.healthy = fresh.healthy
	// Keep the more recent probe timestamp so the reaper doesn't treat
	// a merge (which doesn't actually re-probe `existing`) as a fresh
	// probe and skip the next health tick.
	if fresh.lastProbe.After(existing.lastProbe) {
		existing.lastProbe = fresh.lastProbe
	}
	existing.lastUsed = time.Now()
}

// bringUp handles everything outside the mutex: capacity check, compose up,
// port readback, health probe, persistence back to the workspace record.
// The caller's ctx bounds compose-up and the health probe: whichever fires
// first (caller cancel, compose-up deadline, or health deadline) aborts the
// provisioning and rolls back the half-started container.
func (m *Manager) bringUp(ctx context.Context, workspaceID, workdir string) (*entry, error) {
	if err := ensureWorkdir(workdir); err != nil {
		return nil, err
	}
	if strings.HasPrefix(workdir, "sandbox://") {
		return nil, fmt.Errorf("sandbox.New: workdir %q is a sandbox-only path; container form requires a host path", workdir)
	}

	projectName := deriveProjectName(workspaceID)

	// `docker compose up -d` may have to pull the sandbox image on a cold
	// host; give it its own larger budget. The health probe that follows
	// is still capped at healthTO so a container that starts but never
	// serves HTTP fails fast. Both derive from ctx so a caller cancel
	// (e.g. aborted HTTP request) kills the bring-up instead of letting
	// a 5-minute compose-up linger.
	upCtx, upCancel := context.WithTimeout(ctx, m.bringUpTO)
	defer upCancel()

	port, err := m.driver.Up(upCtx, upRequest{
		WorkspaceID: workspaceID,
		ProjectName: projectName,
		HostWorkdir: workdir,
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox.New: compose up failed for workspace %s: %w", workspaceID, err)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	readyCtx, readyCancel := context.WithTimeout(ctx, m.healthTO)
	defer readyCancel()
	if err := waitReady(readyCtx, baseURL); err != nil {
		// Teardown must NOT inherit the caller's ctx — if the caller
		// cancelled us mid-probe, we still want compose down to run to
		// completion so we don't leak the half-started container.
		downCtx, downCancel := context.WithTimeout(context.Background(), m.healthTO)
		_ = m.driver.Down(downCtx, projectName)
		downCancel()
		return nil, fmt.Errorf("sandbox.New: container health-check failed for workspace %s: %w", workspaceID, err)
	}

	// Persist the binding BEFORE publishing the entry so recover() can
	// adopt this container after a quartet restart. If persistence fails
	// we tear the container down here — leaving it running would orphan
	// it: the next acquire would spin up a new container for the same
	// workspace while this one lingers, since recover() only adopts
	// containers whose workspace meta still carries a SandboxRef.
	if err := m.persistRef(workspaceID, &typesmodel.SandboxRef{
		ProjectName: projectName,
		Template:    defaultTemplate,
	}); err != nil {
		downCtx, downCancel := context.WithTimeout(context.Background(), m.healthTO)
		_ = m.driver.Down(downCtx, projectName)
		downCancel()
		return nil, fmt.Errorf("sandbox.New: persist sandbox ref for workspace %s failed: %w", workspaceID, err)
	}

	ent := &entry{
		workspaceID: workspaceID,
		projectName: projectName,
		baseURL:     baseURL,
		workdir:     workdir,
		healthy:     true,
		lastProbe:   time.Now(),
	}

	logger.Info("[sandbox.Manager] container up: workspace=%s project=%s url=%s", workspaceID, projectName, baseURL)
	return ent, nil
}

// releaseEntry drops one lease against a Container-form entry. Entries
// whose count hits zero become candidates for the idle reaper.
func (m *Manager) releaseEntry(ent *entry) {
	if ent == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.entries[ent.workspaceID]
	if !ok || cur != ent {
		// The workspace's entry has been replaced (e.g. old container died and
		// a new one was brought up). Never apply a lease release to the new entry.
		return
	}
	if ent.refCount > 0 {
		ent.refCount--
	}
	ent.lastUsed = time.Now()
}

// evictIfOverCapLocked enforces the global container cap. Must be called
// with m.mu held. Evicts the oldest idle entry (refCount==0) as LRU;
// active entries are never kicked out. Evicted entries stay in the map
// with tearing=true until tearDownAndRemove finishes, so concurrent
// acquires for the same workspace correctly wait for the teardown.
func (m *Manager) evictIfOverCapLocked(keepWorkspaceID string) {
	if m.capacity <= 0 {
		return
	}
	alive := 0
	for _, ent := range m.entries {
		if !ent.tearing {
			alive++
		}
	}
	if alive <= m.capacity {
		return
	}
	type candidate struct {
		id   string
		last time.Time
	}
	var cands []candidate
	for id, ent := range m.entries {
		if id == keepWorkspaceID {
			continue
		}
		if ent.refCount > 0 || ent.tearing {
			continue
		}
		cands = append(cands, candidate{id, ent.lastUsed})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].last.Before(cands[j].last) })

	for _, c := range cands {
		if alive <= m.capacity {
			return
		}
		victim := m.entries[c.id]
		victim.tearing = true
		victim.tornDone = make(chan struct{})
		alive--
		m.teardownWG.Add(1)
		// Cap concurrency via reaperTeardownSem (shared with the idle
		// reaper) so a large one-shot eviction batch can't fan out a
		// docker-compose-down per victim and saturate the docker daemon.
		// The semaphore is acquired INSIDE the goroutine — doing it here
		// while holding m.mu would deadlock: teardownAndRemove needs m.mu
		// to clean up m.entries, and any goroutine already holding a
		// semaphore slot can't get there until we release m.mu.
		safe.Go(context.Background(), func() {
			defer m.teardownWG.Done()
			reaperTeardownSem <- struct{}{}
			defer func() { <-reaperTeardownSem }()
			m.tearDownAndRemove(context.Background(), victim)
		})
		logger.Info("[sandbox.Manager] capacity LRU evict: workspace=%s project=%s", c.id, victim.projectName)
	}
}

// tearDown runs compose down for the given entry outside the lock. Used
// by paths that never registered the entry in m.entries (Shutdown,
// bringUp rollback); the reaper / capacity eviction instead goes through
// tearDownAndRemove so it can also clear the map slot and release
// waiters on entry.tornDone.
//
// The caller's ctx is honoured: its deadline (if any) caps the compose
// down, and the local 2*healthTO cap is applied on top so a missing
// deadline still bounds the operation.
func (m *Manager) tearDown(ctx context.Context, ent *entry) {
	if ctx == nil {
		ctx = context.Background()
	}
	downCtx, cancel := context.WithTimeout(ctx, 2*m.healthTO)
	defer cancel()
	if err := m.driver.Down(downCtx, ent.projectName); err != nil {
		logger.Warn("[sandbox.Manager] compose down failed: workspace=%s project=%s err=%v",
			ent.workspaceID, ent.projectName, err)
		return
	}
	logger.Info("[sandbox.Manager] container down: workspace=%s project=%s", ent.workspaceID, ent.projectName)
	// Clear the persisted workspace→container binding so recovery after a
	// restart doesn't try to adopt a container that no longer exists.
	// Failures here are non-fatal — next boot's recovery will notice the
	// container is gone and self-heal via a fresh bringUp.
	if err := m.persistRef(ent.workspaceID, nil); err != nil {
		logger.Warn("[sandbox.Manager] clear sandbox ref failed: workspace=%s err=%v", ent.workspaceID, err)
	}
}

// tearDownAndRemove runs compose down then removes the entry from
// m.entries and signals any waiting acquire. Always closes tornDone on
// exit so waiters never hang, even if docker is unreachable.
func (m *Manager) tearDownAndRemove(ctx context.Context, ent *entry) {
	if ctx == nil {
		ctx = context.Background()
	}
	downCtx, cancel := context.WithTimeout(ctx, 2*m.healthTO)
	defer cancel()
	downOK := true
	if err := m.driver.Down(downCtx, ent.projectName); err != nil {
		logger.Warn("[sandbox.Manager] compose down failed: workspace=%s project=%s err=%v",
			ent.workspaceID, ent.projectName, err)
		downOK = false
	} else {
		logger.Info("[sandbox.Manager] container down: workspace=%s project=%s", ent.workspaceID, ent.projectName)
	}
	if downOK {
		if err := m.persistRef(ent.workspaceID, nil); err != nil {
			logger.Warn("[sandbox.Manager] clear sandbox ref failed: workspace=%s err=%v", ent.workspaceID, err)
		}
	}
	m.mu.Lock()
	if m.entries[ent.workspaceID] == ent {
		delete(m.entries, ent.workspaceID)
	}
	if ent.tornDone != nil {
		close(ent.tornDone)
	}
	m.mu.Unlock()
}

// persistRef writes the workspace→container binding back to disk. The
// returned error is non-nil if the binding could not be recorded, in
// which case the caller MUST tear the container down: an unpersisted
// container is invisible to recover() after a process restart and will
// be orphaned by the next acquire() for the same workspace. When a
// SandboxRefSink is configured (production path) the sink is
// responsible for keeping the workspace service's in-memory copy in
// sync with disk; otherwise we fall back to the repo, which is enough
// for tests that don't run a full workspace service.
func (m *Manager) persistRef(workspaceID string, ref *typesmodel.SandboxRef) error {
	if sink := currentRefSink(); sink != nil {
		if err := sink.SetSandboxRef(workspaceID, ref); err != nil {
			return fmt.Errorf("sink.SetSandboxRef: %w", err)
		}
		return nil
	}
	repo, err := m.repoFactory()
	if err != nil {
		return fmt.Errorf("open workspace repo: %w", err)
	}
	if err := repo.SetSandboxRef(workspaceID, ref); err != nil {
		return fmt.Errorf("repo.SetSandboxRef: %w", err)
	}
	return nil
}

// Shutdown stops the reaper and tears every live container down (unless
// QUARTET_SANDBOX_KEEP_ON_EXIT=1). Called from the process shutdown
// path; safe to call multiple times. Waits for any in-flight async
// teardown (capacity evictions) so the process doesn't exit mid-compose,
// then tears the remaining entries down concurrently so one slow compose
// down doesn't starve later workspaces of the shutdown budget.
//
// The caller's ctx caps total shutdown time: if docker hangs at the OS
// level (daemon deadlock) the compose-down timeouts can't kill the child
// process, so we stop waiting on ctx.Done() rather than block the process
// exit forever. A nil or never-cancelled ctx falls back to a 30s local
// deadline.
func (m *Manager) Shutdown(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	if m.managerCancel != nil {
		m.managerCancel()
	}
	if m.reaperCancel != nil {
		m.reaperCancel()
	}
	ents := make([]*entry, 0, len(m.entries))
	for _, ent := range m.entries {
		// Skip entries whose teardown is already in flight; the tracked
		// goroutine will finish them and teardownWG.Wait() below gates
		// our exit on that.
		if ent.tearing {
			continue
		}
		ents = append(ents, ent)
	}
	m.entries = map[string]*entry{}
	m.mu.Unlock()

	if m.reaperDone != nil {
		select {
		case <-m.reaperDone:
		case <-ctx.Done():
			logger.Warn("[sandbox.Manager] Shutdown: reaper did not exit within deadline; continuing with final teardown")
		}
	}
	if !waitWithCtx(ctx, &m.teardownWG) {
		logger.Warn("[sandbox.Manager] Shutdown: in-flight teardowns did not finish within deadline; continuing with final teardown")
	}
	if m.keepOnClose {
		logger.Info("[sandbox.Manager] keep-on-exit set; leaving %d containers running", len(ents))
		return
	}
	// Use a fresh bounded context for the final teardown so that an already
	// exhausted parent ctx (e.g. the reaper/teardownWG waits above burned
	// the full budget) doesn't cause every driver.Down call to fail-fast
	// and leak the containers.
	finalCtx := ctx
	if finalCtx.Err() != nil {
		var cancel context.CancelFunc
		finalCtx, cancel = context.WithTimeout(context.Background(), 2*m.healthTO)
		defer cancel()
	}
	var wg sync.WaitGroup
	for _, ent := range ents {
		wg.Add(1)
		go func(ent *entry) {
			defer wg.Done()
			m.tearDown(finalCtx, ent)
		}(ent)
	}
	if !waitWithCtx(finalCtx, &wg) {
		logger.Warn("[sandbox.Manager] Shutdown: final container teardown did not finish within deadline; some containers may still be running")
	}
}

// waitWithCtx blocks until wg.Wait() returns or ctx fires. Reports true
// when the WaitGroup completed in time, false otherwise.
func waitWithCtx(ctx context.Context, wg *sync.WaitGroup) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// Shutdown is a package-level convenience so cmd/web/main.go doesn't need
// a reference to the singleton. After Shutdown returns, the cached
// singleton is cleared so a later GetFileManager()/acquire call will
// construct a fresh Manager — supporting in-process hot reload and tests.
//
// When no Manager has ever been constructed (e.g. the process crashed
// before serving any sandbox request) Shutdown is a no-op: otherwise we
// would spin one up — starting the reaper and running recovery — only
// to immediately tear it down again.
func Shutdown(ctx context.Context) {
	managerMu.Lock()
	m := managerInst
	managerMu.Unlock()
	if m == nil {
		return
	}
	m.Shutdown(ctx)
	resetManagerSingleton()
}

// newLocalClient mirrors the old Local-form behaviour: in-process wrapper
// around the upstream localSandbox, no container bookkeeping.
func newLocalClient(workdir string) (*Client, error) {
	client := localSandbox.NewClient(localSandbox.WithCwd(workdir))
	ctx, err := client.GetContext()
	if err != nil {
		return nil, fmt.Errorf("sandbox.New: local get context failed: %w", err)
	}
	return &Client{
		Client:  client,
		Ctx:     ctx,
		Workdir: workdir,
		release: func() {},
	}, nil
}

// ensureWorkdir makes sure the host-side workdir exists before we bind
// mount it. Missing-but-creatable directories are created; anything else
// (file-in-place, permission) is returned as an error.
//
// The workdir is validated to be an absolute, canonical path with no
// traversal segments so a malformed workspace record (or a caller that
// synthesises a path from an untrusted ID) can't trick MkdirAll into
// creating directories outside the intended root.
func ensureWorkdir(workdir string) error {
	if workdir == "" {
		return errors.New("empty workdir")
	}
	if !filepath.IsAbs(workdir) {
		return fmt.Errorf("workdir %q must be absolute", workdir)
	}
	// filepath.Clean collapses ".." and "." segments; if the cleaned form
	// differs the original was not canonical, which is suspicious enough
	// to refuse. Legitimate workspace configs store a clean path.
	if filepath.Clean(workdir) != workdir {
		return fmt.Errorf("workdir %q must be a canonical path", workdir)
	}
	info, err := os.Stat(workdir)
	if err != nil {
		if os.IsNotExist(err) {
			// Group-writable (0o770) instead of world-writable: the sandbox
			// container runs as a different uid than the quartet process,
			// so we need group access, but other local users on the host
			// shouldn't be able to read or modify workspace contents.
			// Operators running containers under a uid/gid that doesn't
			// share a group with the quartet process must arrange group
			// membership themselves (or override this function).
			if mkErr := os.MkdirAll(workdir, 0o770); mkErr != nil {
				return fmt.Errorf("create workdir %s failed: %w", workdir, mkErr)
			}
			// MkdirAll is still subject to process umask; enforce the final mode.
			if chErr := os.Chmod(workdir, 0o770); chErr != nil {
				return fmt.Errorf("chmod workdir %s failed: %w", workdir, chErr)
			}
			return nil
		}
		return fmt.Errorf("stat workdir %s failed: %w", workdir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workdir %s is not a directory", workdir)
	}
	return nil
}

// deriveProjectName turns a workspace ID into a compose-safe project id.
// Compose requires lowercase alphanumerics plus underscore/hyphen.
func deriveProjectName(workspaceID string) string {
	const prefix = "quartet-sb-"
	// Compose's project name must be <= 63 chars and only contain [a-z0-9_-].
	// We also need stability + collision resistance: different workspace IDs
	// like "ws/1" and "ws:1" would otherwise normalize to the same name.
	//
	// Strategy:
	//  1) normalize to a safe base
	//  2) append a short sha256 suffix of the original input
	//  3) truncate to fit 63 chars
	base := strings.Builder{}
	for _, r := range strings.ToLower(workspaceID) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			base.WriteRune(r)
		case r == '-' || r == '_':
			base.WriteRune(r)
		default:
			base.WriteByte('-')
		}
	}
	baseStr := strings.Trim(base.String(), "-_")
	if baseStr == "" {
		baseStr = "ws"
	}
	sum := sha256.Sum256([]byte(workspaceID))
	// 12 hex chars = 48 bits; more than enough to avoid collisions in practice.
	suffix := hex.EncodeToString(sum[:6])

	// Ensure <= 63 chars.
	// final = prefix + baseStr + "-" + suffix
	maxBase := 63 - len(prefix) - 1 - len(suffix)
	if maxBase < 1 {
		maxBase = 1
	}
	if len(baseStr) > maxBase {
		baseStr = baseStr[:maxBase]
		baseStr = strings.TrimRight(baseStr, "-_")
		if baseStr == "" {
			baseStr = "ws"
		}
	}
	return prefix + baseStr + "-" + suffix
}

// containerWorkdir is the in-container path where a workspace's host
// workdir is bind-mounted. Kept in lock-step with template.go. Callers
// are expected to have passed workspaceID through repository.validateID
// already; the rejects below are defensive and would represent a bug if
// they fired, not a user error.
func containerWorkdir(workspaceID string) string {
	if workspaceID == "" ||
		strings.ContainsRune(workspaceID, 0) ||
		strings.ContainsAny(workspaceID, "/\\") ||
		strings.Contains(workspaceID, "..") {
		// Fail loud: falling back silently would let a caller stuff a
		// traversal-laden id into a container path used as bind-mount
		// target / chroot root.
		panic(fmt.Sprintf("sandbox: invalid workspace id for container workdir: %q", workspaceID))
	}
	return "/home/sandbox/workspaces/" + workspaceID
}
