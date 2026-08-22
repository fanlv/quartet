package handler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fanlv/quartet/services/agent/acp"
	"github.com/fanlv/quartet/services/agent/catalog"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/config"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

type reconcileSettingsService struct {
	config.SettingsService
	clearCalls []string
}

func (s *reconcileSettingsService) ClearAgentSettings(agentID string) error {
	s.clearCalls = append(s.clearCalls, agentID)
	return nil
}

type reconcileWorkspaceService struct {
	workspace.Service
	clearCalls []string
	clearErr   error
}

func (s *reconcileWorkspaceService) ClearAgentDefaults(agentID string) error {
	s.clearCalls = append(s.clearCalls, agentID)
	return s.clearErr
}

type reconcileACPService struct {
	acp.ACPService
	deleteCalls [][]string
}

func (s *reconcileACPService) DeleteByAgentIdentifiers(identifiers []string) int {
	s.deleteCalls = append(s.deleteCalls, append([]string(nil), identifiers...))
	return 0
}

func TestReconcileDeletingAgents_RestartClearsWorkspaceDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	t.Setenv("HOME", filepath.Join(root, "home"))
	ctx := context.Background()
	const (
		agentID = "custom-restart-delete"
		modelID = "model-before-delete"
	)

	beforeRestart, err := workspace.NewService()
	if err != nil {
		t.Fatalf("create workspace service before restart: %v", err)
	}
	defaultWorkspace, ok := beforeRestart.Get(consts.DefaultWorkspaceID)
	if !ok {
		t.Fatalf("default workspace %q not found", consts.DefaultWorkspaceID)
	}
	if _, err := beforeRestart.Update(
		defaultWorkspace.ID,
		defaultWorkspace.Title,
		defaultWorkspace.Description,
		defaultWorkspace.Workdir,
		agentID,
		modelID,
	); err != nil {
		t.Fatalf("persist workspace defaults before restart: %v", err)
	}

	beforeRestartCatalog, err := catalog.NewService()
	if err != nil {
		t.Fatalf("create Agent catalog before restart: %v", err)
	}
	if err := beforeRestartCatalog.SaveCustom(ctx, []model.CustomAgent{deletingAgentForReconcileTest(agentID)}); err != nil {
		t.Fatalf("persist deleting Agent before restart: %v", err)
	}

	// Construct fresh services from disk to model the startup recovery path.
	afterRestart, err := workspace.NewService()
	if err != nil {
		t.Fatalf("reload workspace service after restart: %v", err)
	}
	afterRestartCatalog, err := catalog.NewService()
	if err != nil {
		t.Fatalf("reload Agent catalog after restart: %v", err)
	}
	probeCache, err := probe.NewCacheService()
	if err != nil {
		t.Fatalf("create ACP probe cache: %v", err)
	}
	settings := &reconcileSettingsService{}
	acpService := &reconcileACPService{}
	h := &Handler{
		agentCatalog:     afterRestartCatalog,
		agentExecutions:  newAgentExecutionGate(),
		settingsService:  settings,
		workspaceService: afterRestart,
		acpProbeCache:    probeCache,
		acpAgentService:  acpService,
	}

	h.reconcileDeletingAgents(ctx)

	reloadedWorkspaces, err := workspace.NewService()
	if err != nil {
		t.Fatalf("reload workspace service after reconciliation: %v", err)
	}
	gotWorkspace, ok := reloadedWorkspaces.Get(consts.DefaultWorkspaceID)
	if !ok {
		t.Fatalf("default workspace %q not found after reconciliation", consts.DefaultWorkspaceID)
	}
	if gotWorkspace.DefaultAgent != "" || gotWorkspace.DefaultModel != "" {
		t.Fatalf(
			"workspace defaults after reconciliation = (%q, %q), want empty",
			gotWorkspace.DefaultAgent,
			gotWorkspace.DefaultModel,
		)
	}
	gotAgent := customAgentForReconcileTest(t, afterRestartCatalog, agentID)
	if gotAgent.Lifecycle != model.AgentLifecycleDeleted {
		t.Fatalf("Agent lifecycle = %q, want %q", gotAgent.Lifecycle, model.AgentLifecycleDeleted)
	}
	if gotAgent.DeleteError != "" {
		t.Fatalf("Agent delete error = %q, want empty", gotAgent.DeleteError)
	}
	if len(settings.clearCalls) != 1 || settings.clearCalls[0] != agentID {
		t.Fatalf("settings cleanup calls = %v, want [%s]", settings.clearCalls, agentID)
	}
	if len(acpService.deleteCalls) != 1 {
		t.Fatalf("ACP cleanup calls = %d, want 1", len(acpService.deleteCalls))
	}
}

func TestReconcileDeletingAgents_WorkspaceCleanupFailureKeepsDeleting(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	ctx := context.Background()
	const agentID = "custom-workspace-cleanup-failure"

	agentCatalog, err := catalog.NewService()
	if err != nil {
		t.Fatalf("create Agent catalog: %v", err)
	}
	if err := agentCatalog.SaveCustom(ctx, []model.CustomAgent{deletingAgentForReconcileTest(agentID)}); err != nil {
		t.Fatalf("persist deleting Agent: %v", err)
	}
	probeCache, err := probe.NewCacheService()
	if err != nil {
		t.Fatalf("create ACP probe cache: %v", err)
	}
	cleanupErr := errors.New("workspace storage unavailable")
	workspaces := &reconcileWorkspaceService{clearErr: cleanupErr}
	acpService := &reconcileACPService{}
	h := &Handler{
		agentCatalog:     agentCatalog,
		agentExecutions:  newAgentExecutionGate(),
		settingsService:  &reconcileSettingsService{},
		workspaceService: workspaces,
		acpProbeCache:    probeCache,
		acpAgentService:  acpService,
	}

	h.reconcileDeletingAgents(ctx)

	gotAgent := customAgentForReconcileTest(t, agentCatalog, agentID)
	if gotAgent.Lifecycle != model.AgentLifecycleDeleting {
		t.Fatalf("Agent lifecycle = %q, want %q", gotAgent.Lifecycle, model.AgentLifecycleDeleting)
	}
	if !gotAgent.DeleteCleanupStarted {
		t.Fatal("DeleteCleanupStarted = false, want true")
	}
	if !strings.Contains(gotAgent.DeleteError, cleanupErr.Error()) {
		t.Fatalf("Agent delete error = %q, want it to contain %q", gotAgent.DeleteError, cleanupErr)
	}
	if len(workspaces.clearCalls) != 1 || workspaces.clearCalls[0] != agentID {
		t.Fatalf("workspace cleanup calls = %v, want [%s]", workspaces.clearCalls, agentID)
	}
	if len(acpService.deleteCalls) != 0 {
		t.Fatalf("ACP cleanup calls = %d, want 0 after workspace cleanup failure", len(acpService.deleteCalls))
	}
	if release, acquired := h.agentExecutions.acquireExecution(agentID); acquired {
		release()
		t.Fatal("Agent execution gate accepted a deleting Agent after cleanup failure")
	}
}

func deletingAgentForReconcileTest(agentID string) model.CustomAgent {
	definition := model.AgentRuntimeDefinition{
		Bin:        "reconcile-test-bin",
		ACPProgram: "reconcile-test-program",
		ACPArgs:    []string{"serve"},
	}
	revision := catalog.RevisionForDefinition(definition)
	return model.CustomAgent{
		AgentID:              agentID,
		DisplayName:          "Reconcile Test Agent",
		Lifecycle:            model.AgentLifecycleDeleting,
		DeleteCleanupStarted: true,
		DeleteError:          "previous cleanup failure",
		CurrentRevision:      revision,
		Revisions: []model.AgentRuntimeRevision{{
			Revision:   revision,
			Definition: definition,
		}},
	}
}

func customAgentForReconcileTest(
	t *testing.T,
	service *catalog.Service,
	agentID string,
) model.CustomAgent {
	t.Helper()
	agents, err := service.Custom(context.Background())
	if err != nil {
		t.Fatalf("read custom Agents: %v", err)
	}
	for _, agent := range agents {
		if agent.AgentID == agentID {
			return agent
		}
	}
	t.Fatalf("custom Agent %q not found", agentID)
	return model.CustomAgent{}
}

var _ config.SettingsService = (*reconcileSettingsService)(nil)
var _ workspace.Service = (*reconcileWorkspaceService)(nil)
var _ acp.ACPService = (*reconcileACPService)(nil)
