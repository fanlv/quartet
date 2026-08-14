package acp

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fanlv/quartet/services/agent/internal/sessioncache"
)

// newTestACPService builds an acpService backed by an unbounded cache and a
// fake agent factory, so GetOrCreate can be exercised without spawning a real
// ACP subprocess.
func newTestACPService(factory newAgentFunc) *acpService {
	return &acpService{
		cache:    sessioncache.New[*ACPAgent](0),
		newAgent: factory,
	}
}

// happyFactory honours the requested construction inputs, like production
// NewACPAgent does when it is the singleflight leader.
func happyFactory(_ context.Context, _ SessionStore, _, agentType, workdir, _, _ string) (*ACPAgent, error) {
	return &ACPAgent{agentType: agentType, workdir: workdir}, nil
}

// Happy path: cache miss, create honours the requested agentType/workdir,
// lease is handed back.
func TestGetOrCreate_CreateHonoursRequestedInputs(t *testing.T) {
	svc := newTestACPService(happyFactory)

	lease, err := svc.GetOrCreate(context.Background(), nil, "ws", "job", "sess", "eino", "/wd")
	if err != nil {
		t.Fatalf("GetOrCreate failed: %v", err)
	}
	defer lease.Release()
	if lease.Value.RequiresRebuild("eino", "/wd") {
		t.Error("freshly created agent must not require rebuild for the inputs it was created with")
	}
}

// Pre-create path contract (already correct today, locked in so the fix keeps
// it symmetric): a cached agent whose construction inputs no longer match AND
// that is mid-Run must surface ErrACPAgentBusyRebuildRequired, never the stale
// agent.
func TestGetOrCreate_PreCreateStaleRunningAgentReturnsBusyError(t *testing.T) {
	svc := newTestACPService(happyFactory)

	lease, err := svc.GetOrCreate(context.Background(), nil, "ws", "job", "sess", "old", "/old")
	if err != nil {
		t.Fatalf("seed GetOrCreate failed: %v", err)
	}
	lease.Value.running.Store(1) // simulate an in-flight Run on the cached agent
	lease.Release()

	_, err = svc.GetOrCreate(context.Background(), nil, "ws", "job", "sess", "new", "/new")
	if !errors.Is(err, ErrACPAgentBusyRebuildRequired) {
		t.Fatalf("expected ErrACPAgentBusyRebuildRequired, got err=%v", err)
	}
}

// BUG PROOF 1: sessioncache.GetOrCreate dedups concurrent creators via
// singleflight — every waiter for the same key shares ONE created agent. When
// the singleflight leader raced in with different construction inputs (a
// concurrent config switch / message send with another agentType or workdir),
// the agent handed to this caller does not match its requested inputs. If that
// shared agent is already mid-Run, the pre-create check's contract says the
// caller must get ErrACPAgentBusyRebuildRequired.
//
// The factory below simulates exactly that hand-off: it returns an agent built
// with STALE inputs that is already running (the leader started its Run in the
// window between create and this caller's re-check).
//
// The buggy post-create check `RequiresRebuild && !IsRunning → rebuild`
// silently falls through when the stale agent IS running and hands it to the
// caller — subsequent Run() then dispatches prompts to the wrong agentType /
// workdir subprocess, the exact failure ErrACPAgentBusyRebuildRequired was
// introduced to prevent.
func TestGetOrCreate_PostCreateStaleRunningAgentMustNotBeReturned(t *testing.T) {
	svc := newTestACPService(func(_ context.Context, _ SessionStore, _, _, _, _, _ string) (*ACPAgent, error) {
		a := &ACPAgent{agentType: "stale", workdir: "/stale"}
		a.running.Store(1) // handed out mid-Run, as a shared singleflight result can be
		return a, nil
	})

	lease, err := svc.GetOrCreate(context.Background(), nil, "ws", "job", "sess", "new", "/new")
	if lease != nil {
		defer lease.Release()
	}
	if !errors.Is(err, ErrACPAgentBusyRebuildRequired) {
		t.Fatalf("expected ErrACPAgentBusyRebuildRequired for a stale mid-Run agent from a shared flight, got err=%v lease=%+v", err, lease)
	}
}

// BUG PROOF 2: the post-create rebuild retries once, but the retried create
// goes through the same race-prone singleflight and its result was never
// re-checked. A second stale winner (or any create that again produces
// mismatched inputs) is handed back as if it were valid.
//
// The factory below always produces an agent with mismatched inputs (and
// counts invocations), simulating consecutive singleflight wins by racing
// callers with stale parameters. The caller must surface an error instead of
// receiving the stale agent.
func TestGetOrCreate_RetryResultStillStaleMustNotBeReturned(t *testing.T) {
	var calls atomic.Int64
	svc := newTestACPService(func(_ context.Context, _ SessionStore, _, _, _, _, _ string) (*ACPAgent, error) {
		calls.Add(1)
		return &ACPAgent{agentType: "stale", workdir: "/stale"}, nil
	})

	lease, err := svc.GetOrCreate(context.Background(), nil, "ws", "job", "sess", "new", "/new")
	if lease != nil {
		defer lease.Release()
	}
	if err == nil {
		t.Fatalf("expected an error when the retried create still yields a mismatched agent, got a live lease for agentType=%q workdir=%q (factory calls=%d)",
			lease.Value.agentType, lease.Value.workdir, calls.Load())
	}
	if calls.Load() < 2 {
		t.Fatalf("expected the rebuild retry to invoke the factory at least twice, got %d", calls.Load())
	}
	if errors.Is(err, ErrACPAgentBusyRebuildRequired) {
		t.Fatalf("non-running stale agent after retry should be a rebuild-non-convergence error, not the busy signal: %v", err)
	}
	if !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("error should explain the rebuild did not converge, got: %v", err)
	}
}
