// Package acpstate converts the stable pkg/acp session-response view into the
// model.* selector state structs shared by the HTTP layer. Both the ACP
// session agent (services/agent/acp) and the session-info probe
// (services/agent/probe) need this conversion; neither imports the other, so
// the canonical implementation lives here as an internal helper under the
// services/agent subtree.
package acpstate

import (
	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/types/model"
)

// Models extracts the model selector list from an ACP session response.
// Returns nil when the response advertises no model selector.
func Models(resp *pkgacp.SessionResponse) *model.SessionModelState {
	if resp == nil {
		return nil
	}
	sel := resp.ModelConfigSelect()
	if sel == nil {
		return nil
	}
	ms := &model.SessionModelState{CurrentModelId: sel.CurrentValue}
	for _, o := range sel.Options {
		ms.AvailableModels = append(ms.AvailableModels, model.ModelInfoACP{
			Description: optDesc(o.Description),
			ModelId:     o.Value,
			Name:        o.Name,
		})
	}
	normalizeCurrentModel(ms)
	return ms
}

func normalizeCurrentModel(ms *model.SessionModelState) {
	if ms == nil {
		return
	}
	if len(ms.AvailableModels) == 0 {
		ms.CurrentModelId = ""
		return
	}
	for _, available := range ms.AvailableModels {
		if available.ModelId == ms.CurrentModelId {
			return
		}
	}
	ms.CurrentModelId = ms.AvailableModels[0].ModelId
}

// Modes extracts the mode selector list from an ACP session response. It
// prefers the standard Modes field and falls back to the "mode" config-option
// select for agents that expose modes there instead. Returns nil when neither
// is present.
func Modes(resp *pkgacp.SessionResponse) *model.SessionModeState {
	if resp == nil {
		return nil
	}
	if resp.Modes != nil {
		ms := &model.SessionModeState{CurrentModeId: string(resp.Modes.CurrentModeID)}
		for _, m := range resp.Modes.AvailableModes {
			ms.AvailableModes = append(ms.AvailableModes, model.ACPSessionMode{
				Description: optDesc(m.Description),
				Id:          string(m.ID),
				Name:        m.Name,
			})
		}
		return ms
	}
	sel := resp.ModeConfigSelect()
	if sel == nil {
		return nil
	}
	ms := &model.SessionModeState{CurrentModeId: sel.CurrentValue}
	for _, o := range sel.Options {
		ms.AvailableModes = append(ms.AvailableModes, model.ACPSessionMode{
			Description: optDesc(o.Description),
			Id:          o.Value,
			Name:        o.Name,
		})
	}
	return ms
}

// ThoughtLevels extracts the thought_level selector list from an ACP session
// response. thought_level has no standard top-level field, so it is sourced
// solely from the "thought_level" config-option select. The select's ConfigID
// is carried through so the setter can target the right config option (e.g.
// "reasoning_effort"). Returns nil when the agent advertises no thought_level
// selector.
func ThoughtLevels(resp *pkgacp.SessionResponse) *model.SessionThoughtLevelState {
	if resp == nil {
		return nil
	}
	sel := resp.ThoughtLevelConfigSelect()
	if sel == nil {
		return nil
	}
	ts := &model.SessionThoughtLevelState{
		CurrentThoughtLevelId: sel.CurrentValue,
		ConfigId:              sel.ConfigID,
	}
	for _, o := range sel.Options {
		ts.AvailableThoughtLevels = append(ts.AvailableThoughtLevels, model.ACPThoughtLevel{
			Description: optDesc(o.Description),
			Id:          o.Value,
			Name:        o.Name,
		})
	}
	return ts
}

// ThoughtLevelConfigID returns the config option id (e.g. "reasoning_effort")
// used to drive thought_level through the generic SetSessionConfigOption API.
// Empty when the agent advertises no thought_level selector.
func ThoughtLevelConfigID(resp *pkgacp.SessionResponse) string {
	if resp == nil {
		return ""
	}
	if sel := resp.ThoughtLevelConfigSelect(); sel != nil {
		return sel.ConfigID
	}
	return ""
}

// ConfigState bundles all three selector lists from a single ACP session
// response. Any subset may be nil when the response omits that selector — a
// SetSessionMode response carries no config options at all, so callers get an
// all-nil state there and keep the prior lists.
func ConfigState(resp *pkgacp.SessionResponse) *model.ACPConfigState {
	return &model.ACPConfigState{
		Models:        Models(resp),
		Modes:         Modes(resp),
		ThoughtLevels: ThoughtLevels(resp),
	}
}

func optDesc(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
