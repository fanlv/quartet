package command

import (
	"testing"

	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/model"
)

// ---- minimal fakes (embed the interfaces; only the methods the command layer
// actually calls are overridden) ----

type fakeWorkspaceService struct {
	workspace.Service
	list []*model.Workspace
}

func (f *fakeWorkspaceService) List() []*model.Workspace { return f.list }

func (f *fakeWorkspaceService) Get(id string) (*model.Workspace, bool) {
	for _, ws := range f.list {
		if ws.ID == id {
			return ws, true
		}
	}
	return nil, false
}

type fakeJobService struct {
	job.Service
	sums []model.JobSummary
	jobs map[string]*model.Job
}

func (f *fakeJobService) ListByWorkspacePaged(wsID, _ string, limit int, _ bool) ([]model.JobSummary, string, bool, int64) {
	var out []model.JobSummary
	for _, s := range f.sums {
		if s.WorkspaceID != wsID {
			continue
		}
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	return out, "", false, 0
}

func (f *fakeJobService) Get(jobID string) (*model.Job, bool) {
	j, ok := f.jobs[jobID]
	return j, ok
}

func newTestExecCtx() *ExecCtx {
	ws := &fakeWorkspaceService{list: []*model.Workspace{
		{ID: "ws-1", Title: "默认", Workdir: "/tmp"},
	}}
	js := &fakeJobService{
		sums: []model.JobSummary{
			{ID: "job-a", Title: "A", WorkspaceID: "ws-1", Status: model.JobStatusCompleted},
			{ID: "job-b", Title: "B", WorkspaceID: "ws-1", Status: model.JobStatusCompleted},
		},
		jobs: map[string]*model.Job{
			"job-a": {ID: "job-a", Title: "A", WorkspaceID: "ws-1", Status: model.JobStatusCompleted},
			"job-b": {ID: "job-b", Title: "B", WorkspaceID: "ws-1", Status: model.JobStatusCompleted},
		},
	}
	return &ExecCtx{
		WorkspaceService:   ws,
		JobService:         js,
		CurrentWorkspaceID: "ws-1",
	}
}

// ---- control cases: behave the same before and after the fix ----

func TestParseASCIIControls(t *testing.T) {
	cases := []struct {
		in       string
		wantCmd  string
		wantArgs string
	}{
		{"/job use 2", "/job", "use 2"},
		{"/job  use  2", "/job", "use  2"},
		{"/HELP", "/help", ""},
		{"hello /help", "", ""},
		{"/etc/hosts", "/etc/hosts", ""},
	}
	for _, c := range cases {
		cmd, args := Parse(c.in)
		if cmd != c.wantCmd || args != c.wantArgs {
			t.Errorf("Parse(%q) = (%q, %q), want (%q, %q)", c.in, cmd, args, c.wantCmd, c.wantArgs)
		}
	}
}

// ---- bug repro: whitespace other than ASCII space must separate tokens ----
//
// The web client detects commands with trimmed.split(/\s+/, 1) (see
// web/src/utils/commands.ts isKnownCommand), so "/job\tlist" IS a known command
// there: the chat page skips the optimistic user bubble and waits for the
// command result. The backend must agree, otherwise the text is forwarded to
// the Agent as a regular message and the user sees nothing.

func TestParseTabSeparator(t *testing.T) {
	cmd, args := Parse("/workspace\tlist")
	if cmd != "/workspace" || args != "list" {
		t.Fatalf("Parse(\"/workspace\\tlist\") = (%q, %q), want (%q, %q)", cmd, args, "/workspace", "list")
	}
}

func TestParseNonBreakingSpace(t *testing.T) {
	cmd, args := Parse("/job use 2")
	if cmd != "/job" || args != "use 2" {
		t.Fatalf("Parse with NBSP = (%q, %q), want (%q, %q)", cmd, args, "/job", "use 2")
	}
}

// Frontend parity: isKnownCommand('/job\tlist') === true in the web client.
func TestIsKnownTabSeparated(t *testing.T) {
	if !IsKnown("/job\tlist") {
		t.Fatal("IsKnown(\"/job\\tlist\") = false, want true (frontend isKnownCommand returns true here)")
	}
	if IsKnown("/unknown\tlist") {
		t.Fatal("IsKnown(\"/unknown\\tlist\") = true, want false")
	}
}

func TestSplitSubTabSeparator(t *testing.T) {
	sub, rest := splitSub("use\t2")
	if sub != "use" || rest != "2" {
		t.Fatalf("splitSub(\"use\\t2\") = (%q, %q), want (%q, %q)", sub, rest, "use", "2")
	}
}

// End-to-end: "/job use<TAB>2" must bind the 2nd job, not reply "Job 不存在".
func TestExecuteJobUseWithTabSeparator(t *testing.T) {
	res, ok := Execute("/job", "use\t2", newTestExecCtx())
	if !ok {
		t.Fatal("Execute returned ok=false for /job")
	}
	if res.Action.Type != ActionBindJob || res.Action.JobID != "job-b" {
		t.Fatalf("Execute(\"/job\", \"use\\t2\") action = %+v, want bind_job job-b; message=%q",
			res.Action, res.Message.Text)
	}
}

// Control for the Execute path: ASCII form keeps working.
func TestExecuteJobUseASCII(t *testing.T) {
	res, ok := Execute("/job", "use 1", newTestExecCtx())
	if !ok {
		t.Fatal("Execute returned ok=false for /job")
	}
	if res.Action.Type != ActionBindJob || res.Action.JobID != "job-a" {
		t.Fatalf("Execute(\"/job\", \"use 1\") action = %+v, want bind_job job-a", res.Action)
	}
}
