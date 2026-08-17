package catalog

import (
	"context"
	"strings"

	"github.com/fanlv/quartet/types/model"
)

// ResolveDisplayInfos resolves display info for historical Agent references
// (session serve commands, graph node snapshot agent types) against the
// retained catalog records. Each identifier resolves in this order:
//
//  1. exact built-in AgentID hit              → Deleted=false
//  2. exact custom AgentID hit (any lifecycle) → Deleted = (lifecycle == deleted)
//  3. built-in migration identifier (stable bin, env key, legacy serve
//     command, explicitly ordered historical identifiers) → Deleted=false
//  4. no match → absent from the result
//
// The result is keyed by the input identifier as given, so callers look up
// whatever reference string they already hold; identical identifiers in the
// input resolve once. The custom catalog is loaded once regardless of how
// many ids are passed.
func (s *Service) ResolveDisplayInfos(ctx context.Context, ids []string) (map[string]model.AgentDisplayInfo, error) {
	result := make(map[string]model.AgentDisplayInfo, len(ids))
	seen := make(map[string]bool, len(ids))
	uniq := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return result, nil
	}

	builtins := BuiltinSnapshot()
	builtinByID := make(map[string]BuiltinAgent, len(builtins))
	for _, agent := range builtins {
		builtinByID[agent.AgentID] = agent
	}

	custom, err := s.Custom(ctx)
	if err != nil {
		return nil, err
	}
	customByID := make(map[string]model.CustomAgent, len(custom))
	for _, agent := range custom {
		customByID[agent.AgentID] = agent
	}

	for _, id := range uniq {
		var info model.AgentDisplayInfo
		found := false
		if builtin, ok := builtinByID[id]; ok {
			info = model.AgentDisplayInfo{
				AgentID:     builtin.AgentID,
				DisplayName: builtin.DisplayName,
				IconURL:     builtin.IconURL,
			}
			found = true
		} else if custom, ok := customByID[id]; ok {
			info = model.AgentDisplayInfo{
				AgentID:     custom.AgentID,
				DisplayName: custom.DisplayName,
				IconURL:     custom.IconURL,
				Deleted:     custom.Lifecycle == model.AgentLifecycleDeleted,
			}
			found = true
		} else if builtin, ok := ResolveBuiltin(id); ok {
			info = model.AgentDisplayInfo{
				AgentID:     builtin.AgentID,
				DisplayName: builtin.DisplayName,
				IconURL:     builtin.IconURL,
			}
			found = true
		}
		if !found {
			continue
		}
		result[id] = info
	}
	return result, nil
}
