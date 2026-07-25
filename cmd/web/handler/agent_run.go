package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/strutil"
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

	lease, err := h.acpAgentService.GetOrCreate(ctx, ss, s.WorkspaceID, s.JobID, s.ID, s.Type, s.Workdir)
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
