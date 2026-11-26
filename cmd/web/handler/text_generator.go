package handler

import (
	"context"
	"fmt"

	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/agent/textgen"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) generateText(ctx context.Context, role model.AgentRoleConfig, prompt, content string) (string, error) {
	resolved, found, err := h.agentCatalog.Resolve(ctx, role.AgentID)
	if err != nil {
		return "", fmt.Errorf("resolve one-shot AgentID %q failed: %w", role.AgentID, err)
	}
	if !found {
		return "", fmt.Errorf("resolve one-shot AgentID %q failed: Agent does not exist", role.AgentID)
	}
	if resolved.Deprecated {
		return "", fmt.Errorf("one-shot AgentID %q is deprecated", resolved.AgentID)
	}
	if resolved.Lifecycle != model.AgentLifecycleActive {
		return "", fmt.Errorf("one-shot AgentID %q lifecycle is %q, want %q", resolved.AgentID, resolved.Lifecycle, model.AgentLifecycleActive)
	}
	if !resolved.SupportsHeadlessPrint {
		return "", fmt.Errorf("one-shot AgentID %q does not support one-shot text generation", resolved.AgentID)
	}
	binding := model.AgentRuntimeBinding{
		AgentID:    resolved.AgentID,
		Revision:   resolved.Revision,
		RuntimeKey: resolved.RuntimeKey,
		Definition: resolved.Definition,
	}
	releaseExecution, ok := h.agentExecutions.acquireExecution(binding.AgentID)
	if !ok {
		return "", fmt.Errorf("one-shot AgentID %q cannot execute: Agent deletion or credential update is in progress", binding.AgentID)
	}
	defer releaseExecution()
	if err := h.ensureBindingAvailable(ctx, binding); err != nil {
		return "", err
	}

	role.AgentID = binding.AgentID
	switch role.AgentID {
	case "codex":
		trackedCtx, release, err := pkgacp.TrackRuntimeOperation(ctx, role.AgentID)
		if err != nil {
			return "", err
		}
		// Account switching waits for the CLI to exit while holding the
		// account store lock. Release the operation before reading the store.
		defer h.saveCodexCredentials(ctx)
		defer release()
		ctx = trackedCtx
	case "opencode":
		// OpenCode 2 identifies a reasoning variant together with its model.
		if role.ModelID == "" && role.ACPThoughtLevel != "" && role.ACPThoughtLevel != "default" {
			envVersion := h.settingsService.GetACPEnvVersion(binding.AgentID)
			if entry, ok := probe.CachedAgentValidation(binding.AgentID, binding.Revision, envVersion); ok && entry.Models != nil {
				role.ModelID = entry.Models.CurrentModelId
			}
		}
	}

	if prompt == "" {
		prompt = content
	} else if content != "" {
		prompt += "\n\n" + content
	}
	return (textgen.Generator{}).Generate(ctx, role, resolved.Bin, prompt, h.settingsService.GetACPEnvVars(role.AgentID))
}
