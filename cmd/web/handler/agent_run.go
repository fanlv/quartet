package handler

import (
	"context"
	"fmt"
	"strconv"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/modelbuilder"
	"github.com/fanlv/quartet/pkg/strutil"
	"github.com/fanlv/quartet/services/agent/eino"
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

func (h *Handler) resolveModelCfg(ctx context.Context, modelID string) (*modelbuilder.ModelConfig, error) {
	if modelID == "" {
		return nil, fmt.Errorf("model_id is required")
	}
	if h.modelConfig == nil {
		return nil, fmt.Errorf("model config is nil")
	}

	id, err := strconv.ParseInt(modelID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid model_id %q: %w", modelID, err)
	}

	inst, err := h.modelConfig.GetModelByID(ctx, id)
	if err != nil {
		return nil, err
	}

	return &modelbuilder.ModelConfig{
		ModelClass:   inst.ModelClass,
		Connection:   inst.Connection,
		ThinkingType: inst.ThinkingType,
	}, nil
}

// runEinoInternal executes the eino agent, usable by Job loop.
func (h *Handler) runEinoInternal(ctx context.Context, s *model.Session, userMessages []*schema.Message, handler agui.EventHandler) error {
	modelCfg, err := h.resolveModelCfg(ctx, s.ModelID)
	if err != nil {
		return fmt.Errorf("resolve model config: %w", err)
	}

	ss, err := h.getOrCreateSessionService(s.WorkspaceID, s.JobID)
	if err != nil {
		return fmt.Errorf("get session service: %w", err)
	}

	lease, err := h.agentService.GetOrCreate(ctx, s.WorkspaceID, s.JobID, s.ID, s.Workdir, modelCfg,
		eino.WithSystemPrompt(s.SystemPrompt),
		eino.WithSessionToucher(ss))
	if err != nil {
		return fmt.Errorf("initialize agent: %w", err)
	}
	// Hold the lease for the full Run so the cache cannot close the
	// agent under us via concurrent eviction or Delete.
	defer lease.Release()

	return lease.Value.Run(ctx, userMessages, handler)
}

// runACPInternal executes the ACP agent, usable by Job loop.
func (h *Handler) runACPInternal(ctx context.Context, s *model.Session, userMessages []*schema.Message, handler agui.EventHandler) error {
	ss, err := h.getOrCreateSessionService(s.WorkspaceID, s.JobID)
	if err != nil {
		return fmt.Errorf("get session service: %w", err)
	}

	lease, err := h.acpAgentService.GetOrCreate(ctx, ss, s.WorkspaceID, s.JobID, s.ID, s.Type, s.Workdir, s.ModelID)
	if err != nil {
		return fmt.Errorf("initialize ACP agent: %w", err)
	}
	// Hold the lease for the full Run so the cache cannot close the
	// agent under us via concurrent eviction or Delete.
	defer lease.Release()

	return lease.Value.Run(ctx, userMessages, handler, s.ModelID, s.ACPMode, s.ACPThoughtLevel, s.JobID)
}
