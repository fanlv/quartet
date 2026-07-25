package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	acp "github.com/eino-contrib/acp"
	"github.com/fanlv/quartet/einocli/config"
	"github.com/fanlv/quartet/einocli/modelbuilder"
)

// Config option ids. The quartet client discovers these from the
// ConfigOptions list by category (model / thought_level) and drives changes
// through session/set_config_option keyed by these ids.
const (
	configIDModel        = "model"
	configIDThoughtLevel = "thought_level"
)

// Thought-level values accepted by set_config_option.
const (
	thoughtLevelAuto    = "auto"
	thoughtLevelEnable  = "enable"
	thoughtLevelDisable = "disable"
)

// configOptions builds the FULL ConfigOptions list for the session: the
// client re-derives every selector from it, so it is always emitted complete
// (never a delta). Every select carries id+name, and every option name+value
// — the client's unmarshal rejects partial entries.
func (a *Agent) configOptions(st *sessionState) ([]acp.SessionConfigOption, error) {
	st.mu.Lock()
	meta := *st.meta
	st.mu.Unlock()

	models, err := config.ListModels()
	if err != nil {
		return nil, fmt.Errorf("list models failed: %w", err)
	}

	out := []acp.SessionConfigOption{modelConfigOption(meta.ModelID, models)}
	if sel, ok := thoughtLevelConfigOption(meta, models); ok {
		out = append(out, sel)
	}
	return out, nil
}

// modelConfigOption is the model selector. An empty catalog yields
// options:[] and currentValue "" — the client shows an empty selector and
// the user is expected to run `eino-cli models add`.
func modelConfigOption(currentModelID string, models []*config.Model) acp.SessionConfigOption {
	options := make([]acp.SessionConfigSelectOption, 0, len(models))
	for _, m := range models {
		desc := string(m.ModelClass)
		if m.Connection != nil {
			desc += "/" + m.Connection.Model
		}
		options = append(options, acp.SessionConfigSelectOption{
			Value:       acp.SessionConfigValueID(m.ID),
			Name:        m.DisplayName,
			Description: desc,
		})
	}
	id := acp.SessionConfigID(configIDModel)
	name := "Model"
	category := acp.SessionConfigOptionCategoryModel
	return acp.NewSessionConfigOptionSelect(acp.SessionConfigOptionSelect{
		SessionConfigSelect: acp.SessionConfigSelect{
			CurrentValue: acp.SessionConfigValueID(currentModelID),
			Options:      acp.NewSessionConfigSelectOptionsSessionConfigSelectOptionList(options),
		},
		ID:       &id,
		Name:     &name,
		Category: &category,
	})
}

// thoughtLevelConfigOption is the thinking selector, advertised ONLY when the
// session's current model class supports a thinking switch. The current value
// is the session override, falling back to the model's own thinking_type,
// falling back to auto.
func thoughtLevelConfigOption(meta sessionMeta, models []*config.Model) (acp.SessionConfigOption, bool) {
	var current *config.Model
	for _, m := range models {
		if m.ID == meta.ModelID {
			current = m
			break
		}
	}
	if current == nil || !modelbuilder.SupportsThoughtLevel(current.ModelClass) {
		return acp.SessionConfigOption{}, false
	}

	value := meta.ThinkingOverride
	if value == "" {
		value = string(current.ThinkingType)
	}
	if value == "" {
		value = thoughtLevelAuto
	}

	id := acp.SessionConfigID(configIDThoughtLevel)
	name := "Thinking"
	category := acp.SessionConfigOptionCategoryThoughtLevel
	return acp.NewSessionConfigOptionSelect(acp.SessionConfigOptionSelect{
		SessionConfigSelect: acp.SessionConfigSelect{
			CurrentValue: acp.SessionConfigValueID(value),
			Options: acp.NewSessionConfigSelectOptionsSessionConfigSelectOptionList([]acp.SessionConfigSelectOption{
				{Name: "Auto", Value: acp.SessionConfigValueID(thoughtLevelAuto)},
				{Name: "Enabled", Value: acp.SessionConfigValueID(thoughtLevelEnable)},
				{Name: "Disabled", Value: acp.SessionConfigValueID(thoughtLevelDisable)},
			}),
		},
		ID:       &id,
		Name:     &name,
		Category: &category,
	}), true
}

// SetSessionConfigOption mutates one session config option (model or
// thought_level) and returns the FULL refreshed ConfigOptions list — the
// client re-derives all selectors from it.
func (a *Agent) SetSessionConfigOption(_ context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	v, ok := req.AsValueID()
	if !ok || v.ConfigID == nil || v.SessionID == nil || v.Value == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.ErrInvalidParams("set_config_option requires sessionId, configId and a value")
	}
	st, err := a.getOrLoadState(string(*v.SessionID))
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}

	configID := string(*v.ConfigID)
	value := string(*v.Value)

	st.mu.Lock()
	switch configID {
	case configIDModel:
		models, err := config.ListModels()
		if err != nil {
			st.mu.Unlock()
			return acp.SetSessionConfigOptionResponse{}, acp.ErrInternalError(fmt.Sprintf("list models failed: %v", err), nil)
		}
		valid := make([]string, 0, len(models))
		found := false
		for _, m := range models {
			valid = append(valid, m.ID)
			if m.ID == value {
				found = true
			}
		}
		if !found {
			st.mu.Unlock()
			return acp.SetSessionConfigOptionResponse{}, acp.ErrInvalidParams(fmt.Sprintf("unknown model %q (valid ids: %s)", value, strings.Join(valid, ", ")))
		}
		st.meta.ModelID = value
		st.meta.UpdatedAt = time.Now().Unix()
		if err := writeMetaLocked(st.dir, st.meta); err != nil {
			st.mu.Unlock()
			return acp.SetSessionConfigOptionResponse{}, acp.ErrInternalError(err.Error(), nil)
		}
	case configIDThoughtLevel:
		if value != thoughtLevelAuto && value != thoughtLevelEnable && value != thoughtLevelDisable {
			st.mu.Unlock()
			return acp.SetSessionConfigOptionResponse{}, acp.ErrInvalidParams(fmt.Sprintf("invalid thought_level %q (valid: auto, enable, disable)", value))
		}
		st.meta.ThinkingOverride = value
		st.meta.UpdatedAt = time.Now().Unix()
		if err := writeMetaLocked(st.dir, st.meta); err != nil {
			st.mu.Unlock()
			return acp.SetSessionConfigOptionResponse{}, acp.ErrInternalError(err.Error(), nil)
		}
	default:
		st.mu.Unlock()
		return acp.SetSessionConfigOptionResponse{}, acp.ErrInvalidParams(fmt.Sprintf("unknown config option %q", configID))
	}
	st.mu.Unlock()

	opts, err := a.configOptions(st)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, acp.ErrInternalError(err.Error(), nil)
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: opts}, nil
}
