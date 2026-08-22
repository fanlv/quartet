package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route"
	"github.com/fanlv/quartet/services/agent/catalog"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/model"
)

func TestWorkspaceCreateRejectsUnknownAndNonActiveDefaultAgents(t *testing.T) {
	for _, lifecycle := range []model.AgentLifecycle{
		model.AgentLifecycleDeleting,
		model.AgentLifecycleDeleted,
	} {
		t.Run(string(lifecycle), func(t *testing.T) {
			h, root := workspaceDefaultsTestHandler(t, []model.CustomAgent{
				workspaceDefaultsTestAgent("custom-not-active", lifecycle),
			})
			status := performWorkspaceCreate(t, h, "custom-not-active", "", filepath.Join(root, "workdir"))
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
			}
		})
	}

	h, root := workspaceDefaultsTestHandler(t, nil)
	status := performWorkspaceCreate(t, h, "custom-unknown", "", filepath.Join(root, "unknown"))
	if status != http.StatusBadRequest {
		t.Fatalf("unknown Agent status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestWorkspaceCreateRejectsDefaultModelWithoutAgent(t *testing.T) {
	h, root := workspaceDefaultsTestHandler(t, nil)
	status := performWorkspaceCreate(t, h, "", "orphan-model", filepath.Join(root, "workdir"))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func workspaceDefaultsTestHandler(t *testing.T, agents []model.CustomAgent) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	t.Setenv("HOME", filepath.Join(root, "home"))
	ctx := context.Background()

	wss, err := workspace.NewService()
	if err != nil {
		t.Fatalf("create workspace service: %v", err)
	}
	agentCatalog, err := catalog.NewService()
	if err != nil {
		t.Fatalf("create Agent catalog: %v", err)
	}
	if err := agentCatalog.SaveCustom(ctx, agents); err != nil {
		t.Fatalf("persist custom Agents: %v", err)
	}
	return &Handler{
		workspaceService: wss,
		agentCatalog:     agentCatalog,
		settingsService:  &reconcileSettingsService{},
	}, root
}

func workspaceDefaultsTestAgent(
	agentID string,
	lifecycle model.AgentLifecycle,
) model.CustomAgent {
	agent := deletingAgentForReconcileTest(agentID)
	agent.Lifecycle = lifecycle
	agent.DeleteCleanupStarted = lifecycle != model.AgentLifecycleActive
	return agent
}

func performWorkspaceCreate(t *testing.T, h *Handler, agentID, modelID, workdir string) int {
	t.Helper()
	engine := route.NewEngine(config.NewOptions(nil))
	engine.POST("/api/v1/workspace/create", h.WorkspaceCreate)
	body, err := json.Marshal(model.CreateWorkspaceRequest{
		Title:        "Workspace",
		Workdir:      workdir,
		DefaultAgent: agentID,
		DefaultModel: modelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := ut.PerformRequest(
		engine,
		http.MethodPost,
		"/api/v1/workspace/create",
		&ut.Body{Body: bytes.NewReader(body), Len: len(body)},
		ut.Header{Key: "Content-Type", Value: "application/json"},
	)
	return recorder.Result().StatusCode()
}
