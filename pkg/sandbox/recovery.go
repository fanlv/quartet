package sandbox

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	typesmodel "github.com/fanlv/quartet/types/model"
)

// recover reconciles the in-memory entry map with surviving containers
// from a previous quartet run. For every workspace whose meta.json
// still carries a SandboxRef we check whether the corresponding compose
// project is still running; if so we adopt it (port re-read via docker),
// if not we leave the ref alone (the next acquire() for that workspace
// re-runs compose up cleanly, reusing the same project name).
//
// Any quartet-owned compose project that is running but no longer
// referenced by a workspace (e.g. workspace was deleted while quartet
// was offline) is torn down at the end so it doesn't linger as an
// unmanaged orphan consuming memory and ports.
//
// This method never fails the boot: docker outages degrade into "no
// containers adopted" rather than a startup error.
func (m *Manager) recover(ctx context.Context) {
	repo, err := m.repoFactory()
	if err != nil {
		logger.Warn("[sandbox.Manager] recover: open workspace repo failed: %v", err)
		return
	}
	workspaces, err := repo.LoadAll()
	if err != nil {
		logger.Warn("[sandbox.Manager] recover: load workspaces failed: %v", err)
		return
	}
	live, err := m.driver.List(ctx)
	if err != nil {
		logger.Warn("[sandbox.Manager] recover: list containers failed: %v", err)
		return
	}
	byProject := make(map[string]listedContainer, len(live))
	for _, c := range live {
		byProject[c.ProjectName] = c
	}

	// Resolve ports and run health probes OUTSIDE the mutex so startup
	// doesn't serialise N*probeTimeout of HTTP calls while holding m.mu.
	type candidate struct {
		ws      *typesmodel.Workspace
		project string
		port    int
		baseURL string
	}
	var cands []candidate
	referenced := make(map[string]struct{}, len(workspaces))
	for _, ws := range workspaces {
		if ws.Sandbox == nil || ws.Sandbox.ProjectName == "" {
			continue
		}
		referenced[ws.Sandbox.ProjectName] = struct{}{}
		c, ok := byProject[ws.Sandbox.ProjectName]
		if !ok || !c.Running {
			continue
		}
		port := c.Port
		if port == 0 {
			// One short retry: `docker inspect` racing with container
			// restart can yield an empty port list even for a running
			// container. The retry is cheap and avoids leaving an
			// otherwise-healthy container permanently orphaned.
			p, err := m.driver.Port(ctx, c.ProjectName)
			if err != nil {
				select {
				case <-time.After(200 * time.Millisecond):
				case <-ctx.Done():
					logger.Warn("[sandbox.Manager] recover: ctx cancelled during port retry: project=%s", c.ProjectName)
					return
				}
				p, err = m.driver.Port(ctx, c.ProjectName)
			}
			if err != nil {
				logger.Warn("[sandbox.Manager] recover: port readback failed for %s after retry: %v (next acquire will re-run compose up)", c.ProjectName, err)
				continue
			}
			port = p
		}
		baseURL := formatBaseURL(port)
		cands = append(cands, candidate{
			ws:      ws,
			project: c.ProjectName,
			port:    port,
			baseURL: baseURL,
		})
	}

	// Probe candidates in parallel before adopting. The previous version
	// marked every adopted container healthy=true without probing, so
	// requests arriving between recovery and the next reaper tick could
	// land on a container whose HTTP server had died. Parallel probing
	// keeps startup bounded (at worst one healthTO per candidate) even with
	// many workspaces.
	healthy := make([]bool, len(cands))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, c := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, baseURL string) {
			defer wg.Done()
			defer func() { <-sem }()
			healthy[i] = probeHealthy(ctx, baseURL, m.healthTO)
		}(i, c.baseURL)
	}
	wg.Wait()

	m.mu.Lock()
	now := time.Now()
	for i, c := range cands {
		if !healthy[i] {
			logger.Warn("[sandbox.Manager] recover: skip unhealthy workspace=%s project=%s (next acquire will re-run compose up)", c.ws.ID, c.project)
			continue
		}
		m.entries[c.ws.ID] = &entry{
			workspaceID: c.ws.ID,
			projectName: c.project,
			baseURL:     c.baseURL,
			// The adopted container's bind-mount was fixed at its
			// original compose-up, and the source directory is not
			// recoverable from `docker ps`. We leave workdir empty;
			// ensureContainer's mismatch check treats empty as
			// "unknown" and skips the warning on reuse.
			workdir:   "",
			healthy:   true,
			lastProbe: now,
			lastUsed:  now,
		}
		logger.Info("[sandbox.Manager] recover: adopted workspace=%s project=%s url=%s", c.ws.ID, c.project, c.baseURL)
	}
	m.mu.Unlock()

	// Orphan sweep: any running quartet-sb-* project not referenced by
	// a workspace's SandboxRef is unmanaged — the reaper never looks at
	// these because they're not in m.entries, and the next Shutdown has
	// no record of them either. Tear them down asynchronously so boot
	// isn't blocked by slow `docker compose down` calls, but gate through
	// teardownWG so Shutdown still waits for them to finish and avoids
	// leaking goroutines across tests.
	for project, c := range byProject {
		if _, ok := referenced[project]; ok {
			continue
		}
		if !c.Running {
			// Stopped orphan: still worth cleaning up the project state
			// so a future workspace with the same id doesn't reuse a
			// stale compose dir, but don't hold up boot on it.
		}
		logger.Info("[sandbox.Manager] recover: tearing down orphan project=%s running=%v", project, c.Running)
		victim := project
		m.teardownWG.Add(1)
		reaperTeardownSem <- struct{}{}
		safe.Go(context.Background(), func() {
			defer m.teardownWG.Done()
			defer func() { <-reaperTeardownSem }()
			downCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := m.driver.Down(downCtx, victim); err != nil {
				logger.Warn("[sandbox.Manager] recover: orphan teardown failed project=%s err=%v", victim, err)
			}
		})
	}
}

func formatBaseURL(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}
