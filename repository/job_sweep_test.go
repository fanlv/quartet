package repository

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

var errSweepDelete = errors.New("forced sweep delete failure")

type sweepDeleteFileManager struct {
	fileserver.FileManager
	fail atomic.Bool
}

func (m *sweepDeleteFileManager) FileDelete(req *fsmodel.FileDeleteRequest) error {
	if m.fail.Load() {
		return errSweepDelete
	}
	return m.FileManager.FileDelete(req)
}

func TestJobRepoSweepDeletedRetriesFailedCleanup(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	const workspaceID = "ws-sweep-deleted"
	repoAPI, err := NewJobRepo(workspaceID)
	if err != nil {
		t.Fatalf("NewJobRepo: %v", err)
	}
	repo := repoAPI.(*jobRepo)

	deleted := model.NewJob(t.TempDir(), workspaceID)
	deleted.ID = "job-deleted"
	deleted.Deleted = true
	if err := repo.Save(deleted.ID, deleted); err != nil {
		t.Fatalf("save deleted job: %v", err)
	}
	live := model.NewJob(t.TempDir(), workspaceID)
	live.ID = "job-live"
	if err := repo.Save(live.ID, live); err != nil {
		t.Fatalf("save live job: %v", err)
	}

	manager := &sweepDeleteFileManager{FileManager: repo.sandbox}
	manager.fail.Store(true)
	repo.sandbox = manager
	if err := repo.SweepDeleted(); err != nil {
		t.Fatalf("best-effort SweepDeleted returned entry error: %v", err)
	}
	deletedDir := typepath.LocalJobDirInWorkspace(workspaceID, deleted.ID)
	if _, err := os.Stat(deletedDir); err != nil {
		t.Fatalf("failed cleanup did not leave tombstone for retry: %v", err)
	}
	loaded, err := repo.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll after failed sweep: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != live.ID {
		t.Fatalf("LoadAll exposed tombstoned job after failed sweep: %#v", loaded)
	}

	manager.fail.Store(false)
	if err := repo.SweepDeleted(); err != nil {
		t.Fatalf("retry SweepDeleted: %v", err)
	}
	if _, err := os.Stat(deletedDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry did not remove tombstoned directory: %v", err)
	}
	liveDir := typepath.LocalJobDirInWorkspace(workspaceID, live.ID)
	if _, err := os.Stat(liveDir); err != nil {
		t.Fatalf("sweep removed live job directory: %v", err)
	}
}
