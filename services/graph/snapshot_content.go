package graph

import (
	"context"
	"fmt"

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
//     ACPThoughtLevel.
//
// A missing model never blocks run start — the snapshot degrades to whatever
// resolved, and the gap is logged. src may be nil (no snapshot capture), in
// which case both maps come back nil.
func buildSnapshotContent(
	ctx context.Context,
	cfg model.GraphConfig,
	src Runner,
	inheritedAgents map[string]model.GraphAgentSnapshot,
) (map[string]string, map[string]model.GraphAgentSnapshot, func(), error) {
	if src == nil {
		return nil, nil, func() {}, nil
	}

	models := map[string]string{}
	agents := map[string]model.GraphAgentSnapshot{}
	var releases []func()
	releaseAll := func() {
		for index := len(releases) - 1; index >= 0; index-- {
			if releases[index] != nil {
				releases[index]()
			}
		}
	}
	for _, n := range cfg.Nodes {
		if !isAgent(n.Type) {
			continue
		}
		if n.Config.SessionStrategy == model.GraphSessionStrategyInherit {
			// An inherited node executes against the upstream session's existing
			// Agent binding. Its own hidden Agent/model fields are irrelevant and
			// must not be resolved into a new runtime snapshot.
			continue
		}
		snapshot := model.GraphAgentSnapshot{
			AgentType:       n.Config.AgentType,
			ModelID:         n.Config.ModelID,
			ACPMode:         n.Config.ACPMode,
			ACPThoughtLevel: n.Config.ACPThoughtLevel,
		}
		if inherited, ok := inheritedAgents[n.ID]; ok {
			inherited.AgentType = n.Config.AgentType
			inherited.ModelID = n.Config.ModelID
			inherited.ACPMode = n.Config.ACPMode
			inherited.ACPThoughtLevel = n.Config.ACPThoughtLevel
			snapshot = inherited
		} else if resolver, ok := src.(AgentSnapshotLeaseResolver); ok {
			resolved, release, err := resolver.ResolveAgentSnapshotWithLease(ctx, n.Config.AgentType)
			if err != nil {
				releaseAll()
				return nil, nil, func() {}, fmt.Errorf(
					"Graph node %q Agent %q is unavailable: %w",
					n.ID,
					n.Config.AgentType,
					err,
				)
			}
			releases = append(releases, release)
			resolved.AgentType = n.Config.AgentType
			resolved.ModelID = n.Config.ModelID
			resolved.ACPMode = n.Config.ACPMode
			resolved.ACPThoughtLevel = n.Config.ACPThoughtLevel
			snapshot = resolved
		} else if resolver, ok := src.(AgentSnapshotResolver); ok {
			resolved, err := resolver.ResolveAgentSnapshot(ctx, n.Config.AgentType)
			if err != nil {
				releaseAll()
				return nil, nil, func() {}, fmt.Errorf(
					"Graph node %q Agent %q is unavailable: %w",
					n.ID,
					n.Config.AgentType,
					err,
				)
			}
			resolved.AgentType = n.Config.AgentType
			resolved.ModelID = n.Config.ModelID
			resolved.ACPMode = n.Config.ACPMode
			resolved.ACPThoughtLevel = n.Config.ACPThoughtLevel
			snapshot = resolved
		}
		agents[n.ID] = snapshot
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
	return models, agents, releaseAll, nil
}
