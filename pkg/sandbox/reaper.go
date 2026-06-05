package sandbox

import (
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
)

// reaperTeardownSem caps the number of `docker compose down` invocations
// the reaper can run in parallel. A full idle sweep with many workspaces
// would otherwise fan out one compose-down per entry and saturate the
// docker daemon / disk.
var reaperTeardownSem = make(chan struct{}, 2)

// startReaper spawns the background goroutine that:
//   - reaps idle entries whose refCount is 0 past the idle timeout
//   - re-probes entries at the configured interval to catch containers
//     that crashed out of band
func (m *Manager) startReaper() {
	ctx, cancel := context.WithCancel(context.Background())
	m.reaperCancel = cancel
	m.reaperDone = make(chan struct{})

	safe.Go(ctx, func() {
		defer close(m.reaperDone)
		tick := minDur(m.idleTimeout, m.healthProbe) / 2
		if tick < 30*time.Second {
			tick = 30 * time.Second
		}
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.reapOnce(ctx)
			}
		}
	})
}

// reapOnce runs a single sweep. It is separated from startReaper so tests
// can drive it deterministically.
func (m *Manager) reapOnce(ctx context.Context) {
	now := time.Now()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	var idle []*entry
	var toProbe []*entry
	for _, ent := range m.entries {
		if ent.tearing {
			continue
		}
		if ent.refCount == 0 && now.Sub(ent.lastUsed) >= m.idleTimeout {
			ent.tearing = true
			ent.tornDone = make(chan struct{})
			idle = append(idle, ent)
			continue
		}
		if now.Sub(ent.lastProbe) >= m.healthProbe {
			toProbe = append(toProbe, ent)
		}
	}
	m.mu.Unlock()

	// Idle teardowns and unhealthy teardowns run through teardownWG so
	// Shutdown gates on them completing. They're dispatched in parallel
	// (mirroring evictIfOverCapLocked) so the reaper tick is not blocked
	// by compose-down latency — critical when there are multiple idle
	// containers, since serialising them would block probing for
	// len(idle)*2*healthTO before the probe sweep even starts.
	//
	// Concurrency is capped via reaperTeardownSem so a large idle batch
	// can't fire N parallel `docker compose down` invocations and crush
	// the docker daemon / I/O.
	//
	// Teardown uses a detached ctx (not the reaper's ctx) because Shutdown
	// cancels the reaper ctx before calling teardownWG.Wait(); inheriting
	// the reaper ctx would make the derived WithTimeout fire immediately
	// and `docker compose down` would be interrupted, leaking the
	// container state. Matches evictIfOverCapLocked.
	for _, ent := range idle {
		logger.Info("[sandbox.Manager] idle reap: workspace=%s project=%s", ent.workspaceID, ent.projectName)
		victim := ent
		m.teardownWG.Add(1)
		reaperTeardownSem <- struct{}{}
		safe.Go(context.Background(), func() {
			defer m.teardownWG.Done()
			defer func() { <-reaperTeardownSem }()
			m.tearDownAndRemove(context.Background(), victim)
		})
	}

	// Probes run in parallel so an unresponsive container's full
	// healthTO/2 doesn't block probing the rest of the pool. At capacity
	// (8 containers) serial probing could take 8*15s=120s vs a 30s tick
	// interval, starving the reaper loop.
	var probeWG sync.WaitGroup
	for _, ent := range toProbe {
		probeWG.Add(1)
		ent := ent
		safe.Go(ctx, func() {
			defer probeWG.Done()
			ok := probeHealthy(ctx, ent.baseURL, m.healthTO/2)
			var teardown *entry
			m.mu.Lock()
			if live, still := m.entries[ent.workspaceID]; still && live == ent {
				live.healthy = ok
				live.lastProbe = time.Now()
				// If the container is unresponsive and no one is using it,
				// mark it for teardown. Leaving an unhealthy entry in the
				// map would make every subsequent acquire retry compose-up
				// against the same broken project without ever freeing the
				// slot.
				if !ok && live.refCount == 0 && !live.tearing {
					live.tearing = true
					live.tornDone = make(chan struct{})
					teardown = live
				}
			}
			m.mu.Unlock()
			if !ok {
				logger.Warn("[sandbox.Manager] probe failed: workspace=%s url=%s", ent.workspaceID, ent.baseURL)
			}
			if teardown != nil {
				logger.Info("[sandbox.Manager] unhealthy reap: workspace=%s project=%s", teardown.workspaceID, teardown.projectName)
				victim := teardown
				// Gate through the same semaphore as idle / eviction
				// teardowns so a probe sweep where multiple containers
				// fail can't fan out more parallel `docker compose
				// down` calls than the configured limit. Acquire
				// OUTSIDE the teardown goroutine so the caller blocks
				// here rather than spawning an unbounded goroutine
				// fleet that all contend on the semaphore.
				m.teardownWG.Add(1)
				reaperTeardownSem <- struct{}{}
				safe.Go(context.Background(), func() {
					defer m.teardownWG.Done()
					defer func() { <-reaperTeardownSem }()
					m.tearDownAndRemove(context.Background(), victim)
				})
			}
		})
	}
	probeWG.Wait()
}

// waitReady blocks until the sandbox HTTP server answers GetContext or the
// context deadline is hit. Used by Manager.bringUp right after compose up.
func waitReady(ctx context.Context, baseURL string) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultHealthTimeout)
	}
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		if probeHealthy(ctx, baseURL, 3*time.Second) {
			return nil
		}
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// probeHealthy is the shared implementation behind waitReady() and the
// periodic reaper probe. Replaces the old package-level TryConnect helper.
//
// Uses a raw context-aware http.Request rather than the SDK's GetContext()
// so cancelling ctx (via Shutdown or reaper eviction) terminates the
// in-flight probe immediately instead of leaving a stranded goroutine for
// the full client timeout. The probe only needs HTTP reachability — the
// body isn't decoded and no auth headers are required for /v1/sandbox.
func probeHealthy(ctx context.Context, baseURL string, timeout time.Duration) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, baseURL+"/v1/sandbox", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	// Drain (rather than just Close) so keep-alive can return the TCP
	// connection to the pool. With healthProbe firing every few seconds
	// across every tracked container, abandoning the body would force
	// a fresh TCP/TLS handshake on every probe.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if d, err := time.ParseDuration(v); err == nil {
		if d <= 0 {
			logger.Warn("[sandbox.Manager] %s=%q must be positive; using default %s", key, v, fallback)
			return fallback
		}
		return d
	}
	logger.Warn("[sandbox.Manager] %s=%q is not a valid duration; using default %s", key, v, fallback)
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			logger.Warn("[sandbox.Manager] %s=%q must be non-negative; using default %d", key, v, fallback)
			return fallback
		}
		return n
	}
	logger.Warn("[sandbox.Manager] %s=%q is not a valid integer; using default %d", key, v, fallback)
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	logger.Warn("[sandbox.Manager] %s=%q is not a valid boolean; using default %v", key, v, fallback)
	return fallback
}
