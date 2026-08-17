package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/strutil"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/session"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) tryUpdateSessionTitleFromUserContent(ctx context.Context, s *model.Session, content string) {
	if s.Title != consts.DefaultSessionTitle && s.Title != "" {
		return
	}

	title := strutil.TruncateRunes(content, 30)
	ss, ok := h.lookupSessionService(s.ID)
	if !ok {
		// Reload failed too — title update is best-effort but we still log so
		// eviction-induced lost updates are observable rather than silent.
		logger.Warnf(ctx, "[session] update title skipped, session not found after reload: sessionId=%s", s.ID)
		return
	}
	if err := ss.UpdateTitle(s.ID, title); err != nil {
		logger.Errorf(ctx, "Failed to update session title: %v", err)
	}
}

// runACPInternal executes the ACP agent, usable by Job loop.
func (h *Handler) runACPInternal(ctx context.Context, s *model.Session, userMessages []*schema.Message, handler agui.EventHandler) error {
	ss, err := h.getOrCreateSessionService(s.WorkspaceID, s.JobID)
	if err != nil {
		return fmt.Errorf("get session service: %w", err)
	}
	releaseExecution, err := h.acquireSessionExecution(ctx, s)
	if err != nil {
		return err
	}
	defer releaseExecution()
	binding, err := h.ensureSessionAgentBinding(ctx, ss, s)
	if err != nil {
		return err
	}
	if err := registerAgentRuntimeBinding(binding); err != nil {
		return fmt.Errorf(
			"register AgentID %q revision %q runtime failed: %w",
			binding.AgentID,
			binding.Revision,
			err,
		)
	}

	// Transient phase reporter: surfaces the "silent" preparation window
	// (subprocess launch / reconnect / history replay / waiting for the
	// first token) to the UI loading label. PublishTransient so it never
	// enters the event buffer and vanishes on refresh — a phase is a
	// current-status hint, not history to replay.
	report := func(phase, detail string) {
		h.jobService.PublishTransient(s.JobID, &model.CustomEvent{
			BaseEvent: model.BaseEvent{
				Type:      model.EventTypeCustom,
				SessionID: s.ID,
				JobID:     s.JobID,
				Timestamp: time.Now().UnixMilli(),
			},
			Name:  model.CustomNameAgentPhase,
			Value: model.AgentPhaseValue{Phase: phase, Detail: detail},
		})
	}

	// Only announce "starting" when a fresh agent will actually be built: a
	// cache hit reuses a live subprocess and skips cold start, so "启动中"
	// would be misleading. Get takes a lease; release it right away since
	// GetOrCreate below takes its own.
	if lease, ok := h.acpAgentService.Get(s.WorkspaceID, s.JobID, s.ID); ok {
		lease.Release()
	} else {
		report(model.AgentPhaseStarting, "")
	}

	lease, err := h.acpAgentService.GetOrCreate(ctx, ss, s.WorkspaceID, s.JobID, s.ID, binding.RuntimeKey, s.Workdir)
	if err != nil {
		initErr := fmt.Errorf("initialize ACP agent: %w", err)
		// ACPAgent.Run normally persists the user turn before prompting, but a
		// constructor failure happens before Run exists. Preserve the input here
		// so refresh/retry cannot make the user's failed turn disappear. The
		// service owns repository access to keep the handler at orchestration level.
		if persistErr := h.acpAgentService.PersistPendingMessages(ctx, ss, s.WorkspaceID, s.JobID, s.ID, userMessages); persistErr != nil {
			return errors.Join(initErr, persistErr)
		}
		return initErr
	}
	// Hold the lease for the full Run so the cache cannot close the
	// agent under us via concurrent eviction or Delete.
	defer lease.Release()

	return lease.Value.Run(ctx, userMessages, handler, s.JobID, report)
}

func (h *Handler) acquireSessionExecution(
	ctx context.Context,
	s *model.Session,
) (func(), error) {
	if s == nil {
		return nil, fmt.Errorf("cannot acquire Agent execution for a nil session")
	}
	agentID := s.AgentID
	revision := s.AgentRevision
	if agentID == "" {
		binding, found, err := h.agentCatalog.ResolveLegacyBinding(ctx, s.Type)
		if err != nil {
			return nil, fmt.Errorf(
				"resolve session %q Agent reference %q before execution failed: %w",
				s.ID,
				s.Type,
				err,
			)
		}
		if !found {
			return nil, fmt.Errorf(
				"session %q Agent reference %q cannot be resolved before execution",
				s.ID,
				s.Type,
			)
		}
		agentID = binding.AgentID
		revision = binding.Revision
	}
	release, acquired := h.agentExecutions.acquireExecution(agentID)
	if !acquired {
		return nil, fmt.Errorf(
			"AgentID %q revision %q cannot execute: Agent deletion is in progress",
			agentID,
			revision,
		)
	}
	return release, nil
}

func (h *Handler) ensureSessionAgentBinding(
	ctx context.Context,
	ss session.Service,
	s *model.Session,
) (model.AgentRuntimeBinding, error) {
	if s.AgentID != "" && s.AgentRevision != "" && s.AgentRuntimeKey != "" &&
		s.AgentDefinition.ACPProgram != "" {
		entry, found, err := h.agentCatalog.Find(ctx, s.AgentID)
		if err != nil {
			return model.AgentRuntimeBinding{}, fmt.Errorf(
				"validate AgentID %q revision %q failed: %w",
				s.AgentID,
				s.AgentRevision,
				err,
			)
		}
		if !found {
			return model.AgentRuntimeBinding{}, fmt.Errorf(
				"AgentID %q revision %q cannot execute: Agent does not exist",
				s.AgentID,
				s.AgentRevision,
			)
		}
		if entry.Source == model.AgentCatalogSourceBuiltin && entry.Builtin != nil && entry.Builtin.Deprecated {
			return model.AgentRuntimeBinding{}, fmt.Errorf(
				"AgentID %q revision %q cannot execute: Agent is deprecated",
				s.AgentID,
				s.AgentRevision,
			)
		}
		if entry.Source == model.AgentCatalogSourceCustom &&
			(entry.Custom == nil || entry.Custom.Lifecycle != model.AgentLifecycleActive) {
			lifecycle := model.AgentLifecycle("")
			if entry.Custom != nil {
				lifecycle = entry.Custom.Lifecycle
			}
			return model.AgentRuntimeBinding{}, fmt.Errorf(
				"AgentID %q revision %q cannot execute: lifecycle=%q",
				s.AgentID,
				s.AgentRevision,
				lifecycle,
			)
		}
		binding := model.AgentRuntimeBinding{
			AgentID:    s.AgentID,
			Revision:   s.AgentRevision,
			RuntimeKey: s.AgentRuntimeKey,
			Definition: s.AgentDefinition,
		}
		if err := registerAgentRuntimeBinding(binding); err != nil {
			return model.AgentRuntimeBinding{}, err
		}
		if err := h.ensureBindingAvailable(ctx, binding); err != nil {
			return model.AgentRuntimeBinding{}, err
		}
		return binding, nil
	}

	binding, found, err := h.agentCatalog.ResolveLegacyBinding(ctx, s.Type)
	if err != nil {
		return model.AgentRuntimeBinding{}, fmt.Errorf(
			"migrate session %q Agent reference %q failed: %w",
			s.ID,
			s.Type,
			err,
		)
	}
	if !found {
		return model.AgentRuntimeBinding{}, fmt.Errorf(
			"session %q Agent reference %q cannot be resolved",
			s.ID,
			s.Type,
		)
	}
	entry, entryFound, entryErr := h.agentCatalog.Find(ctx, binding.AgentID)
	if entryErr != nil {
		return model.AgentRuntimeBinding{}, fmt.Errorf(
			"validate migrated session %q AgentID %q failed: %w",
			s.ID,
			binding.AgentID,
			entryErr,
		)
	}
	if !entryFound {
		return model.AgentRuntimeBinding{}, fmt.Errorf(
			"session %q AgentID %q revision %q cannot execute: Agent does not exist",
			s.ID,
			binding.AgentID,
			binding.Revision,
		)
	}
	if entry.Source == model.AgentCatalogSourceBuiltin && entry.Builtin != nil && entry.Builtin.Deprecated {
		return model.AgentRuntimeBinding{}, fmt.Errorf(
			"session %q AgentID %q revision %q cannot execute: Agent is deprecated",
			s.ID,
			binding.AgentID,
			binding.Revision,
		)
	}
	if entry.Source == model.AgentCatalogSourceCustom &&
		(entry.Custom == nil || entry.Custom.Lifecycle != model.AgentLifecycleActive) {
		lifecycle := model.AgentLifecycle("")
		if entry.Custom != nil {
			lifecycle = entry.Custom.Lifecycle
		}
		return model.AgentRuntimeBinding{}, fmt.Errorf(
			"session %q AgentID %q revision %q cannot execute: lifecycle=%q",
			s.ID,
			binding.AgentID,
			binding.Revision,
			lifecycle,
		)
	}
	if err := ss.UpdateAgentBinding(s.ID, binding); err != nil {
		return model.AgentRuntimeBinding{}, fmt.Errorf(
			"persist session %q AgentID %q revision %q failed: %w",
			s.ID,
			binding.AgentID,
			binding.Revision,
			err,
		)
	}
	if err := registerAgentRuntimeBinding(binding); err != nil {
		return model.AgentRuntimeBinding{}, err
	}
	if err := h.ensureBindingAvailable(ctx, binding); err != nil {
		return model.AgentRuntimeBinding{}, err
	}
	return binding, nil
}

func registerAgentRuntimeBinding(binding model.AgentRuntimeBinding) error {
	if err := pkgacp.RegisterAgentRuntime(binding.RuntimeKey, pkgacp.RuntimeDefinition{
		Program: binding.Definition.ACPProgram,
		Args:    binding.Definition.ACPArgs,
		EnvKey:  binding.AgentID,
	}); err != nil {
		return fmt.Errorf(
			"register AgentID %q revision %q runtime failed: %w",
			binding.AgentID,
			binding.Revision,
			err,
		)
	}
	return nil
}

func (h *Handler) ensureBindingAvailable(ctx context.Context, binding model.AgentRuntimeBinding) error {
	installed := (agentinstall.Checker{}).Check(agentinstall.Definition{
		Bin:        binding.Definition.Bin,
		ACPProgram: binding.Definition.ACPProgram,
	})
	if !installed.Installed {
		return fmt.Errorf(
			"AgentID %q revision %q is not installed: %s",
			binding.AgentID,
			binding.Revision,
			installed.Error,
		)
	}
	envVersion := h.settingsService.GetACPEnvVersion(binding.AgentID)
	if cached, matched := probe.CachedAgentValidation(binding.AgentID, binding.Revision, envVersion); matched {
		if cached.Success {
			if h.acpProbeCache.PersistPending() {
				if err := h.acpProbeCache.PersistNow(ctx); err != nil {
					return fmt.Errorf(
						"AgentID %q revision %q is available in memory but its ACP validation cache still cannot be persisted: %w",
						binding.AgentID,
						binding.Revision,
						err,
					)
				}
			}
			return nil
		}
		return fmt.Errorf(
			"AgentID %q revision %q is unavailable: %s",
			binding.AgentID,
			binding.Revision,
			cached.Error,
		)
	}
	result, err := probe.ValidateBinding(ctx, binding, envVersion, nil)
	persistErr := h.acpProbeCache.PersistNow(ctx)
	if err != nil {
		if persistErr != nil {
			err = errors.Join(err, fmt.Errorf("persist ACP validation result failed: %w", persistErr))
		}
		return fmt.Errorf(
			"AgentID %q revision %q validation failed: %w",
			binding.AgentID,
			binding.Revision,
			err,
		)
	}
	if !result.Success {
		return fmt.Errorf(
			"AgentID %q revision %q validation failed: %s",
			binding.AgentID,
			binding.Revision,
			result.Error,
		)
	}
	if persistErr != nil {
		return fmt.Errorf(
			"AgentID %q revision %q validation succeeded but persisting the result failed: %w",
			binding.AgentID,
			binding.Revision,
			persistErr,
		)
	}
	return nil
}
