package ios

import (
	"strings"
	"testing"
)

func TestWorkspaceDefaultsUseVersionedPatchWithoutPreflightGet(t *testing.T) {
	models := chatSource(t, "Quartet/Core/Models/APIModels.swift")
	client := chatSource(t, "Quartet/Core/Networking/APIClient.swift")
	appModel := chatSource(t, "Quartet/App/AppModel.swift")

	for _, contract := range []string{
		"var version: UInt64",
		"let expectedVersion: UInt64",
		"let defaultAgent: String",
		"let defaultModel: String",
	} {
		if !strings.Contains(models, contract) {
			t.Fatalf("workspace versioned patch DTO missing %q", contract)
		}
	}
	requestStart := strings.Index(models, "struct UpdateWorkspaceRequest")
	requestEnd := strings.Index(models[requestStart:], "struct AgentSummary")
	if requestStart < 0 || requestEnd < 0 {
		t.Fatal("cannot locate UpdateWorkspaceRequest source")
	}
	requestBody := models[requestStart : requestStart+requestEnd]
	for _, forbidden := range []string{"let title:", "let description:", "let workdir:"} {
		if strings.Contains(requestBody, forbidden) {
			t.Fatalf("defaults patch must not include unrelated field %q", forbidden)
		}
	}
	updateStart := strings.Index(client, "func updateWorkspaceDefaults")
	updateEnd := strings.Index(client[updateStart:], "func agents()")
	if updateStart < 0 || updateEnd < 0 {
		t.Fatal("cannot locate updateWorkspaceDefaults implementation")
	}
	updateBody := client[updateStart : updateStart+updateEnd]
	for _, contract := range []string{
		`method: "PATCH"`,
		"expectedVersion: workspace.version",
	} {
		if !strings.Contains(updateBody, contract) {
			t.Fatalf("workspace defaults request missing %q", contract)
		}
	}
	if strings.Contains(updateBody, `request(path: "api/v1/workspace/\(workspace.id)")`) {
		t.Fatal("workspace defaults update must not perform a preflight GET")
	}
	if !strings.Contains(appModel, "updateWorkspaceDefaults(") || !strings.Contains(appModel, "workspaces[index] = saved") {
		t.Fatal("AppModel must commit the server-returned workspace and version")
	}
	for _, contract := range []string{
		"candidate.available",
		"available.models?.availableModels.contains",
		"defaultAgent: canonicalAgent",
		"defaultModel: validModel",
	} {
		if !strings.Contains(appModel, contract) {
			t.Fatalf("workspace defaults validation missing %q", contract)
		}
	}
}
