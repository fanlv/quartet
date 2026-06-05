package acp

import (
	"context"
	"errors"
	"fmt"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/agent/internal/sessioncache"
)

const (
	envMaxACPAgents     = "QUARTET_MAX_ACP_AGENTS"
	defaultMaxACPAgents = 64
)

// ErrACPAgentCapacityExceeded is returned when the in-memory ACP agent cache
// is at capacity and there is no idle agent to evict. Wraps the shared
// sentinel so existing `errors.Is(err, ErrACPAgentCapacityExceeded)`
// sites continue to match.
var ErrACPAgentCapacityExceeded = fmt.Errorf("acp %w", sessioncache.ErrCapacityExceeded)

// ErrACPAgentBusyRebuildRequired is returned when a fresh request asks for
// an ACP agent whose construction inputs (agentType / workdir / preset Coco
// model) differ from a cached agent that is currently mid-Run. Those inputs
// are baked into the subprocess + tracked-conn at NewACPAgent() time and
// cannot be hot-swapped, so silently returning the stale instance would
// either dispatch to the wrong subprocess or keep using the originally
// pinned model on Coco. The session-level Run gate normally prevents
// concurrent Runs in the happy path; surface a typed error here for the
// rare collision instead of silently using a stale agent.
var ErrACPAgentBusyRebuildRequired = errors.New("acp agent rebuild required but a Run is in flight; retry after the previous Run completes")

// Lease is a borrowed reference to a cached *ACPAgent. Callers obtain
// one from GetOrCreate / Get and MUST Release it (via `defer
// lease.Release()`) when the operation that needs the agent is done.
// While outstanding, the cache will not Close() the agent under the
// caller, even if eviction or Delete is triggered concurrently.
type Lease = sessioncache.Lease[*ACPAgent]

// ACPService manages ACPAgent instances per session.
type ACPService interface {
	GetOrCreate(ctx context.Context, store SessionStore, wsID, jobID, sessionID, agentType, workdir, acpModelID string) (*Lease, error)
	Get(wsID, jobID, sessionID string) (*Lease, bool)
	Delete(wsID, jobID, sessionID string)
}

type acpService struct {
	cache *sessioncache.Cache[*ACPAgent]
}

func NewACPService() ACPService {
	cache := sessioncache.New[*ACPAgent](sessioncache.EnvInt(envMaxACPAgents, defaultMaxACPAgents))
	// Preserve the old Infof on reuse so ops can still correlate a
	// follow-up Run with the specific acpSession it attached to. Uses
	// the locked SessionID() getter — reading a.acpSession directly
	// would race with the writers in reconnectIfNeeded / resetACPSession.
	cache = cache.WithReuseLog(func(key string, a *ACPAgent) {
		logger.Infof(context.Background(),
			"[ACPService] reuse ACP agent, key=%s acpSession=%s",
			key, a.SessionID())
	})
	return &acpService{cache: cache}
}

// agentCacheKey composes the cache key from the full identity tuple
// (workspace, job, session). Using sessionID alone here used to allow
// two different jobs that happened to mint the same microsecond-precision
// sessionID to alias to one another's cached agent — including the wrong
// ChatContextRepo, sandbox, and ACP subprocess.
func agentCacheKey(wsID, jobID, sessionID string) string {
	return wsID + "/" + jobID + "/" + sessionID
}

func (s *acpService) GetOrCreate(ctx context.Context, store SessionStore, wsID, jobID, sessionID, agentType, workdir, acpModelID string) (*Lease, error) {
	key := agentCacheKey(wsID, jobID, sessionID)

	// agentType / workdir are baked into the subprocess + tracked-conn at
	// construction; for Coco, acpModelID is also baked in via
	// presetCocoModel because SetSessionModel does not take effect on the
	// live session. If the cached agent's construction inputs no longer
	// match, returning it would either dispatch to the wrong subprocess
	// (agentType / workdir change) or silently keep using the originally
	// pinned model on Coco. Mid-Run mismatch surfaces a typed error
	// rather than silently reusing the stale agent — closing under an
	// in-flight Run would surface as an opaque "client closed" error.
	// The session-level Run gate already keeps Runs serial per session
	// in the normal path, so the mid-Run mismatch path is rare.
	if existing, ok := s.cache.Get(key); ok {
		if existing.Value.RequiresRebuild(agentType, workdir, acpModelID) {
			if existing.Value.IsRunning() {
				logger.Warnf(ctx, "[ACPService] rebuild required but agent is mid-Run, refusing stale reuse: key=%s type=%s workdir=%s model=%s", key, agentType, workdir, acpModelID)
				existing.Release()
				return nil, ErrACPAgentBusyRebuildRequired
			}
			logger.Infof(ctx, "[ACPService] rebuild required, dropping cached agent: key=%s type=%s workdir=%s model=%s", key, agentType, workdir, acpModelID)
			// Drop our lease before deleting so the cache can close the
			// stale entry promptly (Delete defers close until refs hit 0).
			existing.Release()
			s.cache.Delete(key)
		} else {
			return existing, nil
		}
	}

	lease, err := s.cache.GetOrCreate(ctx, key, func(createCtx context.Context) (*ACPAgent, error) {
		logger.Infof(createCtx, "[ACPService] create ACP agent, key=%s", key)
		return NewACPAgent(createCtx, store, sessionID, agentType, workdir, jobID, wsID, acpModelID)
	})
	if err != nil {
		if errors.Is(err, sessioncache.ErrCapacityExceeded) {
			return nil, fmt.Errorf("%w", ErrACPAgentCapacityExceeded)
		}
		return nil, err
	}
	// A concurrent caller could have lost the rebuild race and won the
	// singleflight create — re-check the resulting agent and rebuild once
	// if the inputs still don't match. Mirrors the eino service's
	// fingerprint re-check in services/agent/eino/manager.go.
	if lease.Value.RequiresRebuild(agentType, workdir, acpModelID) && !lease.Value.IsRunning() {
		logger.Infof(ctx, "[ACPService] rebuild required after create, retrying once: key=%s type=%s workdir=%s model=%s", key, agentType, workdir, acpModelID)
		lease.Release()
		s.cache.Delete(key)
		lease, err = s.cache.GetOrCreate(ctx, key, func(createCtx context.Context) (*ACPAgent, error) {
			return NewACPAgent(createCtx, store, sessionID, agentType, workdir, jobID, wsID, acpModelID)
		})
		if err != nil {
			if errors.Is(err, sessioncache.ErrCapacityExceeded) {
				return nil, fmt.Errorf("%w", ErrACPAgentCapacityExceeded)
			}
			return nil, err
		}
	}
	return lease, nil
}

func (s *acpService) Get(wsID, jobID, sessionID string) (*Lease, bool) {
	return s.cache.Get(agentCacheKey(wsID, jobID, sessionID))
}

func (s *acpService) Delete(wsID, jobID, sessionID string) {
	s.cache.Delete(agentCacheKey(wsID, jobID, sessionID))
}
