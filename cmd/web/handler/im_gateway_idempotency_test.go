package handler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/messaging"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

type imIdempotencyWorkspaceService struct {
	workspace.Service
	ws *model.Workspace
}

func (s *imIdempotencyWorkspaceService) Get(id string) (*model.Workspace, bool) {
	if s.ws == nil || s.ws.ID != id {
		return nil, false
	}
	cp := *s.ws
	return &cp, true
}

func (s *imIdempotencyWorkspaceService) List() []*model.Workspace {
	if s.ws == nil {
		return nil
	}
	cp := *s.ws
	return []*model.Workspace{&cp}
}

type imIdempotencyRunner struct {
	calls atomic.Int32
}

func (*imIdempotencyRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-im", nil
}

func (r *imIdempotencyRunner) RunIteration(context.Context, string, []*schema.Message, agui.EventHandler) error {
	r.calls.Add(1)
	return nil
}

func (*imIdempotencyRunner) SessionModelID(string) string { return "" }

func TestIMFirstMessageRedeliveryReusesJobAndRunsAgentOnce(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	ws := &model.Workspace{ID: "ws-im", Workdir: t.TempDir()}
	workspaces := &imIdempotencyWorkspaceService{ws: ws}
	jobs, err := job.NewService(workspaces)
	if err != nil {
		t.Fatalf("job.NewService: %v", err)
	}
	gateway := &imGateway{h: &Handler{
		workspaceService: workspaces,
		jobService:       jobs,
		recentDirsRepo:   createJobRecentDirsRepo{},
	}}
	msg := &messaging.Message{Platform: messaging.PlatformLark, ChatID: "chat-1", MessageID: "msg-1", Content: "hello"}
	mappingWithoutJob := &repository.IMJobMapping{Platform: "lark", ChatID: "chat-1", WorkspaceID: ws.ID}
	config := &model.IMSessionAgentConfig{ModelID: "model-1"}

	first, _, err := gateway.resolveJob(context.Background(), msg, mappingWithoutJob, config, "codex")
	if err != nil {
		t.Fatalf("first resolveJob: %v", err)
	}
	// Simulate a crash before chat->Job mapping persistence: the redelivery
	// again reaches resolveJob with no JobID. The create key must recover the
	// exact same durable Job instead of creating an orphan.
	second, _, err := gateway.resolveJob(context.Background(), msg, mappingWithoutJob, config, "codex")
	if err != nil {
		t.Fatalf("redelivery resolveJob: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("redelivery created different jobs: %q != %q", first.ID, second.ID)
	}

	opts := &job.SendMessageOptions{
		SessionID:       "session-im",
		ClientMessageID: imClientMessageID(msg),
		Messages:        []*schema.Message{schema.UserMessage(msg.Content)},
		AgentType:       "codex",
		ModelID:         config.ModelID,
	}
	runner := &imIdempotencyRunner{}
	firstSend, err := jobs.SendMessage(context.Background(), first.ID, runner, opts)
	if err != nil || !firstSend.Started() {
		t.Fatalf("first SendMessage=(%#v,%v), want started", firstSend, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		receipt, found, lookupErr := jobs.LookupMessage(first.ID, opts)
		if lookupErr != nil {
			t.Fatalf("LookupMessage: %v", lookupErr)
		}
		if found && receipt.State == model.ClientMessageStateCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for completed receipt: found=%t receipt=%#v", found, receipt)
		}
		time.Sleep(time.Millisecond)
	}
	duplicate, err := jobs.SendMessage(context.Background(), second.ID, runner, opts)
	if err != nil || duplicate.Disposition != job.SendMessageDuplicate {
		t.Fatalf("redelivery SendMessage=(%#v,%v), want duplicate", duplicate, err)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("Agent executions=%d, want 1", got)
	}
}
