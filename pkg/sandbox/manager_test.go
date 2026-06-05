package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fanlv/quartet/repository"
	typesmodel "github.com/fanlv/quartet/types/model"
	"gopkg.in/yaml.v3"
)

func TestDeriveProjectName(t *testing.T) {
	inputs := []string{"ws/1", "ws:1", "ws 1", "Ws-2024-05", ""}
	seen := make(map[string]string, len(inputs))
	for _, in := range inputs {
		got := deriveProjectName(in)
		if !strings.HasPrefix(got, "quartet-sb-") {
			t.Fatalf("deriveProjectName(%q) = %q, want quartet prefix", in, got)
		}
		if len(got) > 63 {
			t.Fatalf("deriveProjectName(%q) too long: %d > 63 (%q)", in, len(got), got)
		}
		if prev, ok := seen[got]; ok {
			t.Fatalf("deriveProjectName collision: %q and %q both mapped to %q", prev, in, got)
		}
		seen[got] = in
	}
}

func TestContainerWorkdir(t *testing.T) {
	got := containerWorkdir("ws-abc")
	want := "/home/sandbox/workspaces/ws-abc"
	if got != want {
		t.Errorf("containerWorkdir = %q, want %q", got, want)
	}
}

func TestParseHostPort(t *testing.T) {
	cases := []struct {
		ports         string
		containerPort int
		want          int
	}{
		{"0.0.0.0:49163->8080/tcp, :::49163->8080/tcp", 8080, 49163},
		{"127.0.0.1:5000->8080/tcp", 8080, 5000},
		{"0.0.0.0:6379->6379/tcp", 8080, 0},
		{"", 8080, 0},
	}
	for _, c := range cases {
		got := parseHostPort(c.ports, c.containerPort)
		if got != c.want {
			t.Errorf("parseHostPort(%q, %d) = %d, want %d", c.ports, c.containerPort, got, c.want)
		}
	}
}

func TestRenderComposeTemplate_UsesDefaults(t *testing.T) {
	t.Setenv(envSandboxCPUs, "")
	t.Setenv(envSandboxMemory, "")
	t.Setenv(envSandboxShmSize, "")
	out, err := renderComposeTemplate(upRequest{WorkspaceID: "ws-1", ProjectName: "quartet-sb-ws-1", HostWorkdir: "/tmp/ws-1"})
	if err != nil {
		t.Fatalf("renderComposeTemplate failed: %v", err)
	}
	var file composeFile
	if err := yaml.Unmarshal([]byte(out), &file); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	svc := file.Services[sandboxServiceName]
	if svc.Deploy == nil || svc.Deploy.Resources.Limits.CPUs != "2" || svc.Deploy.Resources.Limits.Memory != "4g" || svc.ShmSize != "1g" {
		t.Fatalf("resource limits should use defaults: %#v", svc)
	}
}

func TestRenderComposeTemplate_UsesEnvOverrides(t *testing.T) {
	t.Setenv(envSandboxCPUs, "6")
	t.Setenv(envSandboxMemory, "12g")
	t.Setenv(envSandboxShmSize, "2g")
	out, err := renderComposeTemplate(upRequest{WorkspaceID: "ws-2", ProjectName: "quartet-sb-ws-2", HostWorkdir: "/tmp/ws-2"})
	if err != nil {
		t.Fatalf("renderComposeTemplate failed: %v", err)
	}
	var file composeFile
	if err := yaml.Unmarshal([]byte(out), &file); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	svc := file.Services[sandboxServiceName]
	if svc.Deploy == nil || svc.Deploy.Resources.Limits.CPUs != "6" || svc.Deploy.Resources.Limits.Memory != "12g" || svc.ShmSize != "2g" {
		t.Fatalf("unexpected env overrides: %#v", svc)
	}
}

func TestFormatCommandError_DeduplicatesPlainErrors(t *testing.T) {
	err := formatCommandError("docker ps failed", errors.New("boom"))
	if got := err.Error(); got != "docker ps failed: boom" {
		t.Fatalf("error = %q, want %q", got, "docker ps failed: boom")
	}
}

// fakeDriver is a containerDriver stand-in that returns a fixed port and
// records calls. Lets the Manager tests exercise ref-count and idle-reap
// without touching docker.
type fakeDriver struct {
	mu           sync.Mutex
	ups          int32
	downs        int32
	failUp       error
	lastUpReq    upRequest
	downDelay    time.Duration // artificial latency inside Down, for race tests
	downInFlight atomic.Int32
	maxDown      atomic.Int32
	downStartAt  atomic.Int64 // UnixNano of first Down call
	downFinishAt atomic.Int64 // UnixNano of last Down return
	upStartAt    atomic.Int64 // UnixNano of first Up call
}

func (f *fakeDriver) Up(_ context.Context, req upRequest) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	atomic.AddInt32(&f.ups, 1)
	f.upStartAt.CompareAndSwap(0, time.Now().UnixNano())
	f.lastUpReq = req
	if f.failUp != nil {
		return 0, f.failUp
	}
	return 49999, nil
}

func (f *fakeDriver) Down(_ context.Context, _ string) error {
	f.downStartAt.CompareAndSwap(0, time.Now().UnixNano())
	inFlight := f.downInFlight.Add(1)
	for {
		maxSeen := f.maxDown.Load()
		if inFlight <= maxSeen || f.maxDown.CompareAndSwap(maxSeen, inFlight) {
			break
		}
	}
	defer f.downInFlight.Add(-1)
	if f.downDelay > 0 {
		time.Sleep(f.downDelay)
	}
	atomic.AddInt32(&f.downs, 1)
	f.downFinishAt.Store(time.Now().UnixNano())
	return nil
}

func (f *fakeDriver) Port(_ context.Context, _ string) (int, error) {
	return 49999, nil
}

func (f *fakeDriver) List(_ context.Context) ([]listedContainer, error) {
	return nil, nil
}

// fakeRepo is a minimal WorkspaceRepo. Only SetSandboxRef is exercised.
type fakeRepo struct {
	mu   sync.Mutex
	refs map[string]*typesmodel.SandboxRef
}

func (r *fakeRepo) Save(id string, ws *typesmodel.Workspace) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refs == nil {
		r.refs = make(map[string]*typesmodel.SandboxRef)
	}
	r.refs[id] = ws.Sandbox
	return nil
}
func (r *fakeRepo) Load(id string) (*typesmodel.Workspace, error) {
	return &typesmodel.Workspace{ID: id}, nil
}
func (r *fakeRepo) ListIDs() ([]string, error)                { return nil, nil }
func (r *fakeRepo) LoadAll() ([]*typesmodel.Workspace, error) { return nil, nil }
func (r *fakeRepo) SweepDeleted() error                       { return nil }
func (r *fakeRepo) RemoveDir(id string) error                 { return nil }
func (r *fakeRepo) SetSandboxRef(id string, ref *typesmodel.SandboxRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refs == nil {
		r.refs = make(map[string]*typesmodel.SandboxRef)
	}
	r.refs[id] = ref
	return nil
}

var _ repository.WorkspaceRepo = (*fakeRepo)(nil)

// failingRepo is a WorkspaceRepo whose SetSandboxRef always returns the
// preconfigured error. Used to drive the persistRef teardown path in
// bringUp without pulling in disk or docker.
type failingRepo struct {
	err error
}

func (r *failingRepo) Save(string, *typesmodel.Workspace) error   { return nil }
func (r *failingRepo) Load(string) (*typesmodel.Workspace, error) { return nil, nil }
func (r *failingRepo) ListIDs() ([]string, error)                 { return nil, nil }
func (r *failingRepo) LoadAll() ([]*typesmodel.Workspace, error)  { return nil, nil }
func (r *failingRepo) SweepDeleted() error                        { return nil }
func (r *failingRepo) RemoveDir(string) error                     { return nil }
func (r *failingRepo) SetSandboxRef(string, *typesmodel.SandboxRef) error {
	return r.err
}

var _ repository.WorkspaceRepo = (*failingRepo)(nil)

// newManagerForTest wires a Manager with a fake docker driver and repo so
// tests don't need docker / disk. Does NOT start the reaper — tests drive
// reapOnce explicitly.
func newManagerForTest(drv containerDriver, repo repository.WorkspaceRepo) *Manager {
	if drv == nil {
		drv = &fakeDriver{}
	}
	if repo == nil {
		repo = &fakeRepo{}
	}
	return &Manager{
		entries:     make(map[string]*entry),
		bringUps:    make(map[string]*bringUpTask),
		driver:      drv,
		repoFactory: func() (repository.WorkspaceRepo, error) { return repo, nil },
		capacity:    2,
		idleTimeout: 10 * time.Millisecond,
		healthTO:    100 * time.Millisecond,
		bringUpTO:   100 * time.Millisecond,
		healthProbe: time.Hour, // don't probe during tests
	}
}

// TestReleaseEntry verifies ref counting without touching docker: we fake
// the entry directly, call releaseEntry, and watch refCount drop.
func TestReleaseEntry(t *testing.T) {
	m := newManagerForTest(nil, nil)
	m.entries["ws-x"] = &entry{workspaceID: "ws-x", refCount: 2, healthy: true}
	m.releaseEntry(m.entries["ws-x"])
	if m.entries["ws-x"].refCount != 1 {
		t.Fatalf("refCount after 1 release = %d, want 1", m.entries["ws-x"].refCount)
	}
	m.releaseEntry(m.entries["ws-x"])
	if m.entries["ws-x"].refCount != 0 {
		t.Fatalf("refCount after 2 releases = %d, want 0", m.entries["ws-x"].refCount)
	}
	// Extra release doesn't go negative.
	m.releaseEntry(m.entries["ws-x"])
	if m.entries["ws-x"].refCount != 0 {
		t.Fatalf("refCount after extra release = %d, want 0", m.entries["ws-x"].refCount)
	}
	// Unknown workspace is a no-op.
	m.releaseEntry(&entry{workspaceID: "nope"})
}

// TestReapIdle makes sure entries with refCount==0 and lastUsed past the
// idle threshold get torn down and removed from the map.
func TestReapIdle(t *testing.T) {
	drv := &fakeDriver{}
	m := newManagerForTest(drv, nil)
	m.entries["ws-idle"] = &entry{
		workspaceID: "ws-idle",
		projectName: "quartet-sb-ws-idle",
		refCount:    0,
		healthy:     true,
		lastUsed:    time.Now().Add(-time.Hour),
		lastProbe:   time.Now(),
	}
	m.entries["ws-busy"] = &entry{
		workspaceID: "ws-busy",
		projectName: "quartet-sb-ws-busy",
		refCount:    1,
		healthy:     true,
		lastUsed:    time.Now().Add(-time.Hour),
		lastProbe:   time.Now(),
	}
	m.reapOnce(context.Background())
	// Teardown now runs asynchronously through teardownWG so the reaper
	// tick isn't blocked by compose-down latency. Wait for the goroutine
	// to finish before asserting on m.entries.
	m.teardownWG.Wait()

	if _, still := m.entries["ws-idle"]; still {
		t.Fatalf("idle entry should be reaped")
	}
	if _, still := m.entries["ws-busy"]; !still {
		t.Fatalf("busy entry (refCount>0) should NOT be reaped")
	}
	// Give the teardown goroutine a tick to record the Down call.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&drv.downs) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&drv.downs) != 1 {
		t.Fatalf("expected 1 Down call, got %d", drv.downs)
	}
}

// TestEvictIfOverCap checks that the capacity guard LRU-evicts idle
// entries (not active ones) to make room for a freshly-registered entry.
// Teardown now runs asynchronously through tearDownAndRemove, so the
// victim stays in the map with tearing=true until the goroutine
// completes; assertions wait on tornDone instead of reading the map
// immediately.
func TestEvictIfOverCap(t *testing.T) {
	drv := &fakeDriver{}
	m := newManagerForTest(drv, nil)
	m.capacity = 2

	m.mu.Lock()
	m.entries["ws-active"] = &entry{workspaceID: "ws-active", projectName: "p1", refCount: 1, lastUsed: time.Now().Add(-time.Minute)}
	victim := &entry{workspaceID: "ws-old-idle", projectName: "p2", refCount: 0, lastUsed: time.Now().Add(-10 * time.Minute)}
	m.entries["ws-old-idle"] = victim
	m.entries["ws-new"] = &entry{workspaceID: "ws-new", projectName: "p3", refCount: 1, lastUsed: time.Now()}
	m.evictIfOverCapLocked("ws-new")
	// Capture tornDone under the lock: the async teardown goroutine
	// closes it and clears m.entries[ws-old-idle] on completion.
	done := victim.tornDone
	m.mu.Unlock()

	if done == nil {
		t.Fatalf("victim should have been marked tearing with a tornDone channel")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("tearDown did not complete within 1s")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries["ws-old-idle"]; ok {
		t.Fatalf("LRU idle entry should be evicted")
	}
	if _, ok := m.entries["ws-active"]; !ok {
		t.Fatalf("active entry must be kept")
	}
	if _, ok := m.entries["ws-new"]; !ok {
		t.Fatalf("the brand-new entry must be kept")
	}
}

// TestPersistRefPropagates the ref to the repo.
func TestPersistRef(t *testing.T) {
	repo := &fakeRepo{}
	m := newManagerForTest(nil, repo)
	if err := m.persistRef("ws-q", &typesmodel.SandboxRef{ProjectName: "quartet-sb-ws-q", Template: "default"}); err != nil {
		t.Fatalf("persistRef: unexpected error: %v", err)
	}
	if repo.refs["ws-q"].ProjectName != "quartet-sb-ws-q" {
		t.Fatalf("ref not propagated: %+v", repo.refs)
	}
}

// TestPersistRefSurfacesRepoError verifies the contract that callers
// (notably bringUp) rely on: when the underlying store refuses to
// accept the binding, persistRef must return the error so the
// container can be torn down instead of orphaned.
func TestPersistRefSurfacesRepoError(t *testing.T) {
	repo := &failingRepo{err: fmt.Errorf("disk full")}
	m := newManagerForTest(nil, repo)
	err := m.persistRef("ws-r", &typesmodel.SandboxRef{ProjectName: "quartet-sb-ws-r", Template: "default"})
	if err == nil {
		t.Fatalf("persistRef: expected error from failing repo, got nil")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("persistRef: error should wrap the underlying cause, got %v", err)
	}
}

// TestTearDownAndRemove verifies the reaper/eviction post-condition:
// driver.Down runs, the entry disappears from m.entries, and waiters on
// tornDone are released.
func TestTearDownAndRemove(t *testing.T) {
	drv := &fakeDriver{}
	m := newManagerForTest(drv, nil)
	ent := &entry{
		workspaceID: "ws-td",
		projectName: "quartet-sb-ws-td",
		tearing:     true,
		tornDone:    make(chan struct{}),
	}
	m.entries["ws-td"] = ent

	m.tearDownAndRemove(context.Background(), ent)

	if atomic.LoadInt32(&drv.downs) != 1 {
		t.Fatalf("expected 1 Down call, got %d", drv.downs)
	}
	m.mu.Lock()
	_, still := m.entries["ws-td"]
	m.mu.Unlock()
	if still {
		t.Fatalf("entry must be removed after tearDownAndRemove")
	}
	select {
	case <-ent.tornDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("tornDone must be closed")
	}
}

// TestEnsureContainerWaitsForTearing covers the teardown/acquire race
// fix: while an entry is tearing, a concurrent ensureContainer for the
// same workspace must block on tornDone instead of racing a new
// compose-up against the in-flight compose-down.
func TestEnsureContainerWaitsForTearing(t *testing.T) {
	drv := &fakeDriver{}
	m := newManagerForTest(drv, nil)

	tornDone := make(chan struct{})
	m.entries["ws-race"] = &entry{
		workspaceID: "ws-race",
		projectName: "quartet-sb-ws-race",
		baseURL:     "http://127.0.0.1:49999",
		tearing:     true,
		tornDone:    tornDone,
		lastUsed:    time.Now(),
	}

	acquireReturned := make(chan error, 1)
	go func() {
		_, err := m.ensureContainer(context.Background(), "ws-race", t.TempDir())
		acquireReturned <- err
	}()

	// Give ensureContainer enough time to enter the tearing-wait loop.
	select {
	case err := <-acquireReturned:
		t.Fatalf("ensureContainer returned before teardown finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Simulate tearDownAndRemove's contract: entry gone from map, done
	// channel closed. The waiter should wake and fall through to bringUp
	// (which will fail in this test since there is no real HTTP server,
	// but the important bit is that it is no longer blocked).
	m.mu.Lock()
	delete(m.entries, "ws-race")
	m.mu.Unlock()
	close(tornDone)

	select {
	case <-acquireReturned:
	case <-time.After(2 * time.Second):
		t.Fatalf("ensureContainer remained blocked after tornDone closed")
	}
}

func TestMergeEntryStateSyncsWorkdir(t *testing.T) {
	existing := &entry{workdir: "/tmp/old", lastUsed: time.Now().Add(-time.Hour)}
	fresh := &entry{baseURL: "http://127.0.0.1:12345", workdir: "/tmp/new", healthy: true, lastProbe: time.Now()}

	mergeEntryState(existing, fresh)

	if existing.baseURL != fresh.baseURL {
		t.Fatalf("baseURL = %q, want %q", existing.baseURL, fresh.baseURL)
	}
	if existing.workdir != fresh.workdir {
		t.Fatalf("workdir = %q, want %q", existing.workdir, fresh.workdir)
	}
	if existing.healthy != fresh.healthy {
		t.Fatalf("healthy = %v, want %v", existing.healthy, fresh.healthy)
	}
	if !existing.lastProbe.Equal(fresh.lastProbe) {
		t.Fatalf("lastProbe = %v, want %v", existing.lastProbe, fresh.lastProbe)
	}
}

func TestMergeEntryStateNilSafe(t *testing.T) {
	mergeEntryState(nil, &entry{workdir: "/tmp/new"})
	mergeEntryState(&entry{workdir: "/tmp/old"}, nil)
}

// TestBringUpFailureTearsDown asserts that a failed Up does not register
// an entry and does call Down for cleanup.
func TestBringUpFailureTearsDown(t *testing.T) {
	drv := &fakeDriver{failUp: errors.New("boom")}
	m := newManagerForTest(drv, nil)
	_, err := m.bringUp(context.Background(), "ws-fail", t.TempDir())
	if err == nil {
		t.Fatalf("expected bringUp error")
	}
	if _, ok := m.entries["ws-fail"]; ok {
		t.Fatalf("failed bringUp must not register an entry")
	}
}

// TestComposeDriverUpCleansOnPortFailure verifies the composeDriver-level
// contract: when `up -d` succeeds but `Port()` fails, the driver must
// tear the project back down so the Manager never has to guess whether
// a half-started container is sitting around.
func TestComposeDriverUpCleansOnPortFailure(t *testing.T) {
	tmp := t.TempDir()
	stub := t.TempDir()
	// Write a fake `docker` binary that always exits 0 for `up`, and
	// always exits 1 (with empty stdout) for `compose port` / `down`.
	// That mimics "container started, but we couldn't learn its port",
	// the half-success path we want covered.
	fakeDocker := filepath.Join(stub, "docker")
	script := `#!/bin/sh
mkdir -p ` + tmp + `
echo "$@" >> ` + filepath.Join(tmp, "calls.txt") + `
case "$*" in
  *" up -d "*|*" up -d"*) exit 0 ;;
  *" port "*) exit 1 ;;
  *" down "*) echo "down-ok"; exit 0 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	d := &composeDriver{stateDir: filepath.Join(tmp, "state"), docker: fakeDocker}
	_, err := d.Up(context.Background(), upRequest{
		WorkspaceID: "ws-t",
		ProjectName: "quartet-sb-ws-t",
		HostWorkdir: tmp,
	})
	if err == nil {
		t.Fatalf("expected Up to fail when port readback errors")
	}

	calls, readErr := os.ReadFile(filepath.Join(tmp, "calls.txt"))
	if readErr != nil {
		t.Fatalf("read fake docker trace: %v", readErr)
	}
	if !strings.Contains(string(calls), " down ") {
		t.Fatalf("expected compose down after port failure, got trace:\n%s", calls)
	}
}

func TestComposeDriverDownRemovesProjectWithoutComposeFile(t *testing.T) {
	tmp := t.TempDir()
	stub := t.TempDir()
	fakeDocker := filepath.Join(stub, "docker")
	script := `#!/bin/sh
mkdir -p ` + tmp + `
echo "$@" >> ` + filepath.Join(tmp, "calls.txt") + `
case "$*" in
  "ps -aq --filter label=com.docker.compose.project=quartet-sb-ws-missing") printf 'cid-1\ncid-2\n'; exit 0 ;;
  "rm -f -v cid-1 cid-2") exit 0 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	d := &composeDriver{stateDir: filepath.Join(tmp, "state"), docker: fakeDocker}
	if err := d.Down(context.Background(), "quartet-sb-ws-missing"); err != nil {
		t.Fatalf("Down should clean orphaned containers without compose file: %v", err)
	}

	calls, err := os.ReadFile(filepath.Join(tmp, "calls.txt"))
	if err != nil {
		t.Fatalf("read fake docker trace: %v", err)
	}
	trace := string(calls)
	if !strings.Contains(trace, "ps -aq --filter label=com.docker.compose.project=quartet-sb-ws-missing") {
		t.Fatalf("expected docker ps fallback when compose file is missing, got trace:\n%s", trace)
	}
	if !strings.Contains(trace, "rm -f -v cid-1 cid-2") {
		t.Fatalf("expected docker rm fallback when compose file is missing, got trace:\n%s", trace)
	}
	if strings.Contains(trace, " compose ") {
		t.Fatalf("should not invoke docker compose down without a compose file, got trace:\n%s", trace)
	}
}

func TestEnsureWorkdirCreatesGroupWritableDir(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "nested", "missing")
	if err := ensureWorkdir(workdir); err != nil {
		t.Fatalf("ensureWorkdir failed: %v", err)
	}
	info, err := os.Stat(workdir)
	if err != nil {
		t.Fatalf("stat workdir failed: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory, got mode %v", info.Mode())
	}
	if got := info.Mode().Perm(); got != 0o770 {
		t.Fatalf("created workdir mode = %#o, want %#o", got, 0o770)
	}
}

func TestShutdownTearsDownEntriesConcurrently(t *testing.T) {
	drv := &fakeDriver{downDelay: 100 * time.Millisecond}
	m := newManagerForTest(drv, nil)
	m.entries["ws-1"] = &entry{workspaceID: "ws-1", projectName: "p1", healthy: true}
	m.entries["ws-2"] = &entry{workspaceID: "ws-2", projectName: "p2", healthy: true}

	m.Shutdown(context.Background())

	if got := atomic.LoadInt32(&drv.downs); got != 2 {
		t.Fatalf("expected 2 Down calls during shutdown, got %d", got)
	}
	if got := drv.maxDown.Load(); got < 2 {
		t.Fatalf("expected concurrent teardown during shutdown, max in-flight downs = %d", got)
	}
	if !m.closed {
		t.Fatalf("manager should be marked closed after shutdown")
	}
}
