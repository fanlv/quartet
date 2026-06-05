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
	if _, err := svc.Update("ws-test", "t2", "d2", bad); err == nil {
		t.Fatalf("Update(bad) unexpectedly succeeded")
	}
	if svc.workspaces["ws-test"].Workdir == bad {
		t.Fatalf("bad workdir applied in memory")
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
