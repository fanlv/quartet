package probe

import (
	"testing"

	"github.com/fanlv/quartet/services/agent/catalog"
	"github.com/fanlv/quartet/types/model"
)

func TestMarkValidationRefreshingClearsResultAcrossEnvironmentVersions(t *testing.T) {
	binding := model.AgentRuntimeBinding{
		AgentID:    "custom-validation-state",
		Revision:   "rev-test",
		RuntimeKey: catalog.RuntimeKey("custom-validation-state", "rev-test"),
	}
	acpValidationCacheMu.Lock()
	acpValidationCache[binding.RuntimeKey] = model.ACPProbeCacheEntry{
		AgentID:     binding.AgentID,
		Revision:    binding.Revision,
		RuntimeKey:  binding.RuntimeKey,
		EnvVersion:  1,
		Success:     true,
		Error:       "old error",
		RefreshedAt: 123,
		Models:      &model.SessionModelState{CurrentModelId: "old-model"},
	}
	acpValidationCacheMu.Unlock()
	t.Cleanup(func() {
		acpValidationCacheMu.Lock()
		delete(acpValidationCache, binding.RuntimeKey)
		acpValidationCacheMu.Unlock()
	})

	markValidationRefreshing(binding, 2)

	entry, matched := CachedAgentValidation(binding.AgentID, binding.Revision, 2)
	if !matched {
		t.Fatal("refreshing validation must match the new environment version")
	}
	if !entry.Refreshing || entry.RefreshedAt != 0 {
		t.Fatalf("refresh state = %+v, want refreshing with no completed result", entry)
	}
	if entry.Success || entry.Error != "" || entry.Models != nil {
		t.Fatalf("new environment inherited the old validation result: %+v", entry)
	}
}

func TestMarkValidationRefreshingKeepsCompletedResultForSameEnvironment(t *testing.T) {
	binding := model.AgentRuntimeBinding{
		AgentID:    "custom-refresh-state",
		Revision:   "rev-test",
		RuntimeKey: catalog.RuntimeKey("custom-refresh-state", "rev-test"),
	}
	acpValidationCacheMu.Lock()
	acpValidationCache[binding.RuntimeKey] = model.ACPProbeCacheEntry{
		AgentID:     binding.AgentID,
		Revision:    binding.Revision,
		RuntimeKey:  binding.RuntimeKey,
		EnvVersion:  3,
		Success:     true,
		RefreshedAt: 456,
	}
	acpValidationCacheMu.Unlock()
	t.Cleanup(func() {
		acpValidationCacheMu.Lock()
		delete(acpValidationCache, binding.RuntimeKey)
		acpValidationCacheMu.Unlock()
	})

	markValidationRefreshing(binding, 3)

	entry, matched := CachedAgentValidation(binding.AgentID, binding.Revision, 3)
	if !matched || !entry.Success || !entry.Refreshing || entry.RefreshedAt != 456 {
		t.Fatalf("same-environment refresh lost the usable result: matched=%t entry=%+v", matched, entry)
	}
}

