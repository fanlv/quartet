package graph

import (
	"context"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// buildSnapshotContent freezes the referenced Agent/model config content of a
// GraphConfig so a GraphRun can replay immune to later global config edits
// (§4 GraphRun 启动保存基线快照). It walks the Agent-class nodes (Prompt and
// Clarify) and captures:
//
//   - ModelSnapshots: keyed by the node's string ModelID, the model's display
//     form (the ACP model identifier itself). Deduplicated across nodes
//     sharing a model.
//   - AgentSnapshots: keyed by node ID, the node's AgentType/ModelID/ACPMode/
//     ACPThoughtLevel plus the resolved (global) system prompt.
//
// A missing model or prompt-resolution error never blocks run start — the
// snapshot degrades to whatever resolved, and the gap is logged. src may be nil
// (no snapshot capture), in which case both maps come back nil.
func buildSnapshotContent(ctx context.Context, cfg model.GraphConfig, src Runner) (map[string]string, map[string]model.GraphAgentSnapshot) {
	if src == nil {
		return nil, nil
	}

	systemPrompt, err := src.ResolveSystemPrompt(ctx)
	if err != nil {
		logger.Warnf(ctx, "[graph] resolve system prompt for snapshot failed (degraded snapshot): %v", err)
		systemPrompt = ""
	}

	models := map[string]string{}
	agents := map[string]model.GraphAgentSnapshot{}
	for _, n := range cfg.Nodes {
		if !isAgent(n.Type) {
			continue
		}
		agents[n.ID] = model.GraphAgentSnapshot{
			AgentType:       n.Config.AgentType,
			ModelID:         n.Config.ModelID,
			ACPMode:         n.Config.ACPMode,
			ACPThoughtLevel: n.Config.ACPThoughtLevel,
			SystemPrompt:    systemPrompt,
		}
		if n.Config.ModelID == "" {
			continue
		}
		if _, ok := models[n.Config.ModelID]; ok {
			continue
		}
		inst, ok := src.ResolveModelSnapshot(ctx, n.Config.ModelID)
		if !ok {
			logger.Warnf(ctx, "[graph] model %q referenced by node %q did not resolve (degraded snapshot)", n.Config.ModelID, n.ID)
			continue
		}
		models[n.Config.ModelID] = inst
	}

	if len(models) == 0 {
		models = nil
	}
	if len(agents) == 0 {
		agents = nil
	}
	return models, agents
}
