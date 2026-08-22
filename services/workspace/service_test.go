package workspace

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

type fakeWorkspaceRepo struct {
	removeErr error
	saveErrOn int
	saves     int
	stored    map[string]*model.Workspace
}

func (r *fakeWorkspaceRepo) Save(id string, ws *model.Workspace) error {
	r.saves++
	if r.saveErrOn > 0 && r.saves == r.saveErrOn {
		return errors.New("save failed")
	}
	if r.stored == nil {
		r.stored = make(map[string]*model.Workspace)
	}
	clone := *ws
	r.stored[id] = &clone
	return nil
}

func (r *fakeWorkspaceRepo) Load(id string) (*model.Workspace, error) {
	if ws, ok := r.stored[id]; ok {
		clone := *ws
		return &clone, nil
	}
	return nil, errors.New("not found")
}

func (r *fakeWorkspaceRepo) ListIDs() ([]string, error) { return nil, nil }

func (r *fakeWorkspaceRepo) LoadAll() ([]*model.Workspace, error) { return nil, nil }

func (r *fakeWorkspaceRepo) SweepDeleted() error { return nil }

func (r *fakeWorkspaceRepo) RemoveDir(id string) error { return r.removeErr }

func (r *fakeWorkspaceRepo) SetSandboxRef(id string, ref *model.SandboxRef) error { return nil }

func TestDeleteRollbackBumpsRevision(t *testing.T) {
	now := time.Now()
	repo := &fakeWorkspaceRepo{}
	svc := &serviceImpl{
		workspaces: map[string]*model.Workspace{
			"ws-test": {
				ID:        "ws-test",
				Title:     "test",
				Workdir:   "/tmp/ws-test",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		repo: repo,
	}

	if err := svc.MarkDeleted("ws-test"); err != nil {
		t.Fatalf("MarkDeleted() error = %v", err)
	}
	markedRevision := svc.Revision()
	if markedRevision == 0 {
		t.Fatalf("expected revision to increase after MarkDeleted")
	}

	repo.removeErr = errors.New("disk busy")
	if err := svc.Delete("ws-test"); err == nil {
		t.Fatalf("Delete() error = nil, want remove failure")
	}

	if got := svc.Revision(); got != markedRevision+1 {
		t.Fatalf("Revision() = %d, want %d after rollback", got, markedRevision+1)
	}
	ws := svc.workspaces["ws-test"]
	if ws == nil {
		t.Fatalf("workspace removed from map after rollback")
	}
	if ws.Deleted {
		t.Fatalf("workspace remains deleted after rollback")
	}
	if repo.saves != 2 {
		t.Fatalf("repo.Save calls = %d, want 2 (mark deleted + rollback)", repo.saves)
	}
}

func TestDeleteRollback_SaveFailsStillRollsMemory(t *testing.T) {
	now := time.Now()
	repo := &fakeWorkspaceRepo{saveErrOn: 2}
	svc := &serviceImpl{
		workspaces: map[string]*model.Workspace{
			"ws-test": {
				ID:        "ws-test",
				Title:     "test",
				Workdir:   "/tmp/ws-test",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		repo: repo,
	}

	if err := svc.MarkDeleted("ws-test"); err != nil {
		t.Fatalf("MarkDeleted() error = %v", err)
	}
	markedRevision := svc.Revision()
	if markedRevision == 0 {
		t.Fatalf("expected revision to increase after MarkDeleted")
	}

	repo.removeErr = errors.New("disk busy")
	if err := svc.Delete("ws-test"); err == nil {
		t.Fatalf("Delete() error = nil, want remove failure")
	}

	if got := svc.Revision(); got != markedRevision+1 {
		t.Fatalf("Revision() = %d, want %d after rollback", got, markedRevision+1)
	}
	ws := svc.workspaces["ws-test"]
	if ws == nil {
		t.Fatalf("workspace removed from map after rollback")
	}
	if ws.Deleted {
		t.Fatalf("workspace remains deleted after rollback even when save failed")
	}
	if repo.saves != 2 {
		t.Fatalf("repo.Save calls = %d, want 2 (mark deleted + rollback attempt)", repo.saves)
	}
}

func TestServiceCreate_ValidatesWorkdir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	t.Setenv("HOME", filepath.Join(root, "home"))

	repo := &fakeWorkspaceRepo{}
	svc := &serviceImpl{workspaces: make(map[string]*model.Workspace), repo: repo}

	wsBad := model.NewWorkspace("t", "d", "evil")
	if err := svc.Create(wsBad); err == nil {
		t.Fatalf("Create(bad) unexpectedly succeeded")
	}
	if _, ok := svc.workspaces[wsBad.ID]; ok {
		t.Fatalf("bad workspace persisted in memory")
	}

	wsGood := model.NewWorkspace("t", "d", filepath.Join(root, "workspaces", "ws-a"))
	if err := svc.Create(wsGood); err != nil {
		t.Fatalf("Create(good) error = %v", err)
	}
	if _, ok := svc.workspaces[wsGood.ID]; !ok {
		t.Fatalf("good workspace not persisted in memory")
	}
}

func TestServiceCreate_PersistsDefaultAgentAndModel(t *testing.T) {
	root := t.TempDir()
	repo := &fakeWorkspaceRepo{}
	svc := &serviceImpl{workspaces: make(map[string]*model.Workspace), repo: repo}
	ws := model.NewWorkspace("t", "d", filepath.Join(root, "workspaces", "ws-prefs"))
	ws.DefaultAgent = "agent-a"
	ws.DefaultModel = "model-a"

	if err := svc.Create(ws); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, ok := svc.Get(ws.ID)
	if !ok {
		t.Fatalf("created workspace not found")
	}
	if got.DefaultAgent != "agent-a" || got.DefaultModel != "model-a" {
		t.Fatalf("Get() prefs = (%q, %q), want (agent-a, model-a)", got.DefaultAgent, got.DefaultModel)
	}
	stored := repo.stored[ws.ID]
	if stored == nil {
		t.Fatalf("workspace not saved")
	}
	if stored.DefaultAgent != "agent-a" || stored.DefaultModel != "model-a" {
		t.Fatalf("stored prefs = (%q, %q), want (agent-a, model-a)", stored.DefaultAgent, stored.DefaultModel)
	}
}

func TestServiceUpdate_ValidatesWorkdir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	t.Setenv("HOME", filepath.Join(root, "home"))

	now := time.Now()
	repo := &fakeWorkspaceRepo{}
	svc := &serviceImpl{
		workspaces: map[string]*model.Workspace{
			"ws-test": {
				ID:        "ws-test",
				Title:     "test",
				Workdir:   filepath.Join(root, "workspaces", "ws-test"),
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		repo: repo,
	}

	bad := "evil"
	if _, err := svc.Update("ws-test", "t2", "d2", bad, "", ""); err == nil {
		t.Fatalf("Update(bad) unexpectedly succeeded")
	}
	if svc.workspaces["ws-test"].Workdir == bad {
		t.Fatalf("bad workdir applied in memory")
	}
}

func TestServiceUpdate_PersistsDefaultAgentAndModel(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	repo := &fakeWorkspaceRepo{}
	svc := &serviceImpl{
		workspaces: map[string]*model.Workspace{
			"ws-test": {
				ID:           "ws-test",
				Title:        "test",
				Workdir:      filepath.Join(root, "workspaces", "ws-test"),
				DefaultAgent: "old-agent",
				DefaultModel: "old-model",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
		repo: repo,
	}

	got, err := svc.Update("ws-test", "t2", "d2", filepath.Join(root, "workspaces", "ws-test"), "agent-b", "model-b")
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got.DefaultAgent != "agent-b" || got.DefaultModel != "model-b" {
		t.Fatalf("Update() prefs = (%q, %q), want (agent-b, model-b)", got.DefaultAgent, got.DefaultModel)
	}
	inMemory := svc.workspaces["ws-test"]
	if inMemory.DefaultAgent != "agent-b" || inMemory.DefaultModel != "model-b" {
		t.Fatalf("in-memory prefs = (%q, %q), want (agent-b, model-b)", inMemory.DefaultAgent, inMemory.DefaultModel)
	}
	stored := repo.stored["ws-test"]
	if stored == nil {
		t.Fatalf("workspace not saved")
	}
	if stored.DefaultAgent != "agent-b" || stored.DefaultModel != "model-b" {
		t.Fatalf("stored prefs = (%q, %q), want (agent-b, model-b)", stored.DefaultAgent, stored.DefaultModel)
	}
}

func TestServiceUpdate_ClearsDefaultAgentAndModel(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	repo := &fakeWorkspaceRepo{}
	svc := &serviceImpl{
		workspaces: map[string]*model.Workspace{
			"ws-test": {
				ID:           "ws-test",
				Title:        "test",
				Workdir:      filepath.Join(root, "workspaces", "ws-test"),
				DefaultAgent: "old-agent",
				DefaultModel: "old-model",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
		repo: repo,
	}

	got, err := svc.Update("ws-test", "t2", "d2", filepath.Join(root, "workspaces", "ws-test"), "", "")
	if err != nil {
		t.Fatalf("Update(clear) error = %v", err)
	}
	if got.DefaultAgent != "" || got.DefaultModel != "" {
		t.Fatalf("Update(clear) prefs = (%q, %q), want empty", got.DefaultAgent, got.DefaultModel)
	}
	inMemory := svc.workspaces["ws-test"]
	if inMemory.DefaultAgent != "" || inMemory.DefaultModel != "" {
		t.Fatalf("in-memory prefs = (%q, %q), want empty", inMemory.DefaultAgent, inMemory.DefaultModel)
	}
	stored := repo.stored["ws-test"]
	if stored == nil {
		t.Fatalf("workspace not saved")
	}
	if stored.DefaultAgent != "" || stored.DefaultModel != "" {
		t.Fatalf("stored prefs = (%q, %q), want empty", stored.DefaultAgent, stored.DefaultModel)
	}
}

func TestClearAgentDefaults_PersistsAndKeepsUnrelatedDefaults(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	repo := &fakeWorkspaceRepo{}
	svc := &serviceImpl{
		workspaces: map[string]*model.Workspace{
			"match": {
				ID:           "match",
				Workdir:      filepath.Join(root, "workspaces", "match"),
				DefaultAgent: "agent-deleted",
				DefaultModel: "model-deleted",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
			"other": {
				ID:           "other",
				Workdir:      filepath.Join(root, "workspaces", "other"),
				DefaultAgent: "agent-kept",
				DefaultModel: "model-kept",
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		},
		repo: repo,
	}

	if err := svc.ClearAgentDefaults("agent-deleted"); err != nil {
		t.Fatalf("ClearAgentDefaults() error = %v", err)
	}

	if got := svc.workspaces["match"]; got.DefaultAgent != "" || got.DefaultModel != "" {
		t.Fatalf("matched prefs = (%q, %q), want empty", got.DefaultAgent, got.DefaultModel)
	}
	if got := svc.workspaces["other"]; got.DefaultAgent != "agent-kept" || got.DefaultModel != "model-kept" {
		t.Fatalf("unrelated prefs = (%q, %q), want kept", got.DefaultAgent, got.DefaultModel)
	}
	stored := repo.stored["match"]
	if stored == nil {
		t.Fatalf("matched workspace not saved")
	}
	if stored.DefaultAgent != "" || stored.DefaultModel != "" {
		t.Fatalf("stored matched prefs = (%q, %q), want empty", stored.DefaultAgent, stored.DefaultModel)
	}
	if _, saved := repo.stored["other"]; saved {
		t.Fatalf("unrelated workspace was unexpectedly saved")
	}
}

func TestFileAccessBaseRoots_IncludesHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("LOCAL_MEMORY", root)
	t.Setenv("HOME", home)

	roots := FileAccessBaseRoots()
	if len(roots) != 2 {
		t.Fatalf("FileAccessBaseRoots() len = %d, want 2", len(roots))
	}
	if roots[0] != root {
		t.Fatalf("FileAccessBaseRoots()[0] = %q, want %q", roots[0], root)
	}
	if roots[1] != home {
		t.Fatalf("FileAccessBaseRoots()[1] = %q, want %q", roots[1], home)
	}
}

func TestTrustedFileWorkspaceRoots_FiltersInvalidWorkdirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	t.Setenv("HOME", filepath.Join(root, "home"))

	good := filepath.Join(root, "workspaces", "ws-good")
	bad := filepath.Join(t.TempDir(), "elsewhere")
	svc := &serviceImpl{
		workspaces: map[string]*model.Workspace{
			"good":  {ID: "good", Workdir: good},
			"bad":   {ID: "bad", Workdir: bad},
			"empty": {ID: "empty", Workdir: ""},
		},
		repo: &fakeWorkspaceRepo{},
	}
	roots := svc.TrustedFileWorkspaceRoots()
	if len(roots) != 2 {
		t.Fatalf("TrustedFileWorkspaceRoots() len = %d, want 2 (%v)", len(roots), roots)
	}
	if roots[0] != good || roots[1] != bad {
		t.Fatalf("TrustedFileWorkspaceRoots() = %v, want [%q %q]", roots, good, bad)
	}
}
