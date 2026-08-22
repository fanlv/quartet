package job

import (
	"errors"
	"os"
	"testing"

	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

func TestStartupSweepsDurableDeletedJob(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	wsSvc, err := workspace.NewService()
	if err != nil {
		t.Fatalf("create workspace service: %v", err)
	}
	ws := model.NewWorkspace("delete recovery", "", t.TempDir())
	ws.ID = "ws-job-delete-recovery"
	if err := wsSvc.Create(ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	firstAPI, err := NewService(wsSvc)
	if err != nil {
		t.Fatalf("create initial job service: %v", err)
	}
	first := firstAPI.(*serviceImpl)
	job := model.NewJob(ws.Workdir, ws.ID)
	job.ID = "job-delete-recovery"
	if err := first.Create(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := first.MarkDeleted(job.ID); err != nil {
		t.Fatalf("persist deleted tombstone: %v", err)
	}

	jobDir := typepath.LocalJobDirInWorkspace(ws.ID, job.ID)
	if _, err := os.Stat(jobDir); err != nil {
		t.Fatalf("job dir must remain before restart sweep: %v", err)
	}

	restarted, err := NewService(wsSvc)
	if err != nil {
		t.Fatalf("restart job service: %v", err)
	}
	if _, ok := restarted.Get(job.ID); ok {
		t.Fatal("deleted job became visible after restart")
	}
	if _, err := os.Stat(jobDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup did not remove tombstoned job directory: %v", err)
	}
}
