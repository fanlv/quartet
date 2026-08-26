package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/fanlv/quartet/types/model"
)

const einoAgentID = "eino-cli"

// initializeEinoUsageModels loads the Eino-owned catalog without reading its
// config file directly. Usage statistics use the provider-facing connection
// model (for example, deepseek-v4-pro-260425), while ACP sessions continue to
// retain the short catalog ID that Eino needs for model selection.
func (h *Handler) initializeEinoUsageModels(ctx context.Context) error {
	models, err := h.einoCLI.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("list Eino models: %w", err)
	}
	aliases := make(map[string]string, len(models))
	for _, item := range models {
		if item == nil || item.Connection == nil {
			continue
		}
		from := strings.TrimSpace(item.ID)
		to := strings.TrimSpace(item.Connection.Model)
		if from == "" || to == "" || from == to {
			continue
		}
		aliases[from] = to
	}

	h.usageModelMu.Lock()
	h.usageModelAliases = aliases
	h.usageModelMu.Unlock()

	if err := h.usageStats.MigrateModelIDs(ctx, aliases); err != nil {
		return fmt.Errorf("migrate Eino usage model IDs: %w", err)
	}
	return nil
}

func (h *Handler) rememberEinoUsageModel(item *model.EinoModel) {
	if item == nil || item.Connection == nil {
		return
	}
	from := strings.TrimSpace(item.ID)
	to := strings.TrimSpace(item.Connection.Model)
	if from == "" || to == "" || from == to {
		return
	}
	h.usageModelMu.Lock()
	if h.usageModelAliases == nil {
		h.usageModelAliases = make(map[string]string)
	}
	h.usageModelAliases[from] = to
	h.usageModelMu.Unlock()
}

func (h *Handler) usageModelID(session *model.Session) string {
	if session == nil {
		return ""
	}
	modelID := strings.TrimSpace(session.ModelID)
	if modelID == "" || !isEinoSession(session) {
		return modelID
	}
	return h.canonicalUsageModelID(modelID)
}

func (h *Handler) canonicalUsageModelID(modelID string) string {
	h.usageModelMu.RLock()
	canonical := h.usageModelAliases[modelID]
	h.usageModelMu.RUnlock()
	if canonical != "" {
		return canonical
	}
	return modelID
}

func isEinoSession(session *model.Session) bool {
	if session.AgentID == einoAgentID || session.Type == einoAgentID || strings.HasPrefix(session.Type, einoAgentID+"@") {
		return true
	}
	return session.AgentDefinition.Bin == einoAgentID || session.AgentDefinition.ACPProgram == einoAgentID
}
