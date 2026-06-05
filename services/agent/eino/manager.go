package eino

import (
	"context"
	"errors"
	"fmt"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/modelbuilder"
	"github.com/fanlv/quartet/services/agent/internal/sessioncache"
)

const (
	envMaxEinoAgents     = "QUARTET_MAX_EINO_AGENTS"
	defaultMaxEinoAgents = 64
)

// ErrAgentCapacityExceeded is returned when the cache is at capacity
// and no idle entry is available for eviction. Wraps the shared
// sentinel so existing `errors.Is` callsites still match.
var ErrAgentCapacityExceeded = fmt.Errorf("eino %w", sessioncache.ErrCapacityExceeded)

// ErrAgentBusyRebuildRequired is returned when a fresh request asks for an
// agent whose construction inputs (model / workdir / system prompt) differ
// from a cached agent that is currently mid-Run. The chatModel and system
// prompt are baked in at New() time and cannot be hot-swapped, so silently
// returning the stale instance would dispatch the request against the wrong
// configuration. Closing the agent under the in-flight Run would surface as
// an opaque "client closed" error, so we surface a typed error instead and
// let the caller decide whether to retry once the previous Run releases
// (the session-level Run gate normally serialises this in the happy path).
var ErrAgentBusyRebuildRequired = errors.New("eino agent rebuild required but a Run is in flight; retry after the previous Run completes")

// Lease is a borrowed reference to a cached *Quartet. Callers obtain
// one from GetOrCreate / Get and MUST Release it (via `defer
// lease.Release()`) when the operation that needs the agent is done.
// While outstanding, the cache will not Close() the agent under the
// caller, even if eviction or Delete is triggered concurrently.
type Lease = sessioncache.Lease[*Quartet]

type Service interface {
	GetOrCreate(ctx context.Context, wsID, jobID, sessionID, workdir string, modelCfg *modelbuilder.ModelConfig, opts ...Option) (*Lease, error)
	Get(wsID, jobID, sessionID string) (*Lease, bool)
	Delete(wsID, jobID, sessionID string)
	List() []*Quartet
}

type service struct {
	cache *sessioncache.Cache[*Quartet]
}

func NewService() Service {
	return &service{
		cache: sessioncache.New[*Quartet](sessioncache.EnvInt(envMaxEinoAgents, defaultMaxEinoAgents)),
	}
}

// agentCacheKey composes the cache key from the full identity tuple
// (workspace, job, session). Using sessionID alone here used to allow
// two different jobs that happened to mint the same microsecond-precision
// sessionID to alias to one another's cached agent — including the wrong
// ChatContextRepo and sandbox.
func agentCacheKey(wsID, jobID, sessionID string) string {
	return wsID + "/" + jobID + "/" + sessionID
}

func (s *service) GetOrCreate(ctx context.Context, wsID, jobID, sessionID, workdir string, modelCfg *modelbuilder.ModelConfig, opts ...Option) (*Lease, error) {
	// Build a fresh slice (cap+3) rather than append-in-place: opts is
	// supplied by the caller and may have spare capacity, in which case
	// a plain append would mutate the caller's backing array.
	createOpts := make([]Option, 0, len(opts)+3)
	createOpts = append(createOpts, opts...)
	createOpts = append(createOpts, WithSessionID(sessionID), WithJobID(jobID), WithWorkspaceID(wsID))

	key := agentCacheKey(wsID, jobID, sessionID)

	// Compute the fingerprint that a fresh agent for this call would
	// have. The cache key is the (ws, job, session) tuple (so an in-flight
	// Run can still find its agent), but if the cached agent was built with
	// a different model / workdir / system prompt, returning it would
	// silently dispatch to the wrong model — the chatModel is captured
	// at New() time and cannot be hot-swapped on an existing instance.
	// On mismatch we evict the stale entry (which Close()s the sandbox
	// + ACP subprocess) and let the singleflight-protected create path
	// rebuild a fresh one.
	fingerprintCfg := optionFingerprintConfig(opts)
	wanted := computeAgentFingerprint(workdir, modelCfg, fingerprintCfg.SystemPrompt)
	if existing, ok := s.cache.Get(key); ok {
		if existing.Value.Fingerprint() == wanted {
			return existing, nil
		}
		// Mismatch + currently streaming: refuse to evict the running
		// agent (closing its sandbox under the goroutine surfaces as an
		// opaque "client closed" error), and refuse to silently reuse it
		// (the chatModel / system prompt / workdir are baked in at New()
		// and would dispatch this request against the wrong config).
		// The session-level Run gate (services/job/executor_run.go)
		// normally prevents concurrent Runs on the same session, so this
		// is a rare collision; surface a typed error and let the caller
		// retry after the in-flight Run completes.
		if existing.Value.IsRunning() {
			logger.Warnf(ctx, "[eino] fingerprint changed but agent is mid-Run, refusing stale reuse: key=%s want=%s have=%s", key, wanted, existing.Value.Fingerprint())
			existing.Release()
			return nil, ErrAgentBusyRebuildRequired
		}
		logger.Infof(ctx, "[eino] fingerprint changed, rebuilding agent: key=%s want=%s have=%s", key, wanted, existing.Value.Fingerprint())
		// Drop our lease before deleting so the cache can close the
		// stale entry promptly (Delete defers close until refs hit 0).
		existing.Release()
		s.cache.Delete(key)
	}

	lease, err := s.cache.GetOrCreate(ctx, key, func(createCtx context.Context) (*Quartet, error) {
		return New(createCtx, workdir, modelCfg, createOpts...)
	})
	if err != nil {
		if errors.Is(err, sessioncache.ErrCapacityExceeded) {
			return nil, fmt.Errorf("%w", ErrAgentCapacityExceeded)
		}
		return nil, err
	}
	// A concurrent caller could have lost the fingerprint race and won
	// the singleflight create — re-check the resulting agent against
	// the wanted fingerprint and evict-then-retry once if needed.
	// Without this re-check, two parallel Runs that both observe a
	// stale entry, both Delete, and both enter cache.GetOrCreate could
	// race such that the loser sees the other's freshly-built agent,
	// and if their wanted fingerprints differ (rare but possible — e.g.
	// model changed mid-flight) the loser would silently use the
	// "wrong" model.
	if lease.Value.Fingerprint() != wanted && !lease.Value.IsRunning() {
		logger.Infof(ctx, "[eino] fingerprint mismatch after create, rebuilding once: key=%s want=%s have=%s", key, wanted, lease.Value.Fingerprint())
		lease.Release()
		s.cache.Delete(key)
		lease, err = s.cache.GetOrCreate(ctx, key, func(createCtx context.Context) (*Quartet, error) {
			return New(createCtx, workdir, modelCfg, createOpts...)
		})
		if err != nil {
			if errors.Is(err, sessioncache.ErrCapacityExceeded) {
				return nil, fmt.Errorf("%w", ErrAgentCapacityExceeded)
			}
			return nil, err
		}
	}
	return lease, nil
}

func (s *service) Get(wsID, jobID, sessionID string) (*Lease, bool) {
	return s.cache.Get(agentCacheKey(wsID, jobID, sessionID))
}

func (s *service) Delete(wsID, jobID, sessionID string) {
	s.cache.Delete(agentCacheKey(wsID, jobID, sessionID))
}

func (s *service) List() []*Quartet {
	return s.cache.List()
}

// optionFingerprintConfig extracts the option fields that change New()'s baked
// runtime wiring so the manager can include them in fingerprint computation
// without re-running the whole option chain at every comparison site.
func optionFingerprintConfig(opts []Option) *Config {
	cfg := &Config{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}
