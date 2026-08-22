package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/model"
)

// fakeJobService embeds job.Service so tests only override the methods the
// exercised path actually calls; any unexpected call panics on the nil
// embedded interface, which is what we want.
type fakeJobService struct {
	job.Service
	mu              sync.Mutex
	titleCalls      []string
	job             *model.Job
	receipts        map[string]job.MessageReceipt
	receiptPayloads map[string]string
	commands        map[string]*model.CommandSystemMessageEvent
	commandPayloads map[string]string
	sendCalls       int
	startCalls      int
	commandCalls    int
	prepareCalls    int
	publishCalls    int
	commandErr      error
}

func (f *fakeJobService) UpdateTitle(jobID string, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titleCalls = append(f.titleCalls, title)
	return nil
}

func (f *fakeJobService) titleCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.titleCalls)
}

func (f *fakeJobService) Get(jobID string) (*model.Job, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job == nil || f.job.ID != jobID {
		return nil, false
	}
	return f.job.DeepCopy(), true
}

func (f *fakeJobService) LookupMessage(_ string, opts *job.SendMessageOptions) (job.MessageReceipt, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	receipt, ok := f.receipts[opts.ClientMessageID]
	payload := firstMessageContent(opts)
	if ok && f.receiptPayloads[opts.ClientMessageID] != payload {
		return job.MessageReceipt{}, false, job.ErrClientMessageIDConflict
	}
	return receipt, ok, nil
}

func (f *fakeJobService) SendMessage(_ context.Context, _ string, runner job.JobRunner, opts *job.SendMessageOptions) (job.SendMessageResult, error) {
	f.mu.Lock()
	f.sendCalls++
	if receipt, ok := f.receipts[opts.ClientMessageID]; ok {
		if f.receiptPayloads[opts.ClientMessageID] != firstMessageContent(opts) {
			f.mu.Unlock()
			if prepared, ok := runner.(job.PreparedExecutionReleaser); ok {
				prepared.ReleasePreparedExecution()
			}
			return job.SendMessageResult{}, job.ErrClientMessageIDConflict
		}
		f.mu.Unlock()
		if prepared, ok := runner.(job.PreparedExecutionReleaser); ok {
			prepared.ReleasePreparedExecution()
		}
		return job.SendMessageResult{Disposition: job.SendMessageDuplicate, Receipt: receipt}, nil
	}
	receipt := job.MessageReceipt{
		ClientMessageID: opts.ClientMessageID,
		State:           model.ClientMessageStateProcessing,
	}
	if f.receipts == nil {
		f.receipts = make(map[string]job.MessageReceipt)
	}
	if f.receiptPayloads == nil {
		f.receiptPayloads = make(map[string]string)
	}
	f.receipts[opts.ClientMessageID] = receipt
	f.receiptPayloads[opts.ClientMessageID] = firstMessageContent(opts)
	f.startCalls++
	f.mu.Unlock()
	if preparer, ok := runner.(job.AcceptedMessagePreparer); ok {
		if err := preparer.PrepareAcceptedMessage(context.Background(), f.job.ID); err != nil {
			return job.SendMessageResult{}, err
		}
		f.mu.Lock()
		f.prepareCalls++
		f.mu.Unlock()
	}
	return job.SendMessageResult{Disposition: job.SendMessageStarted, Receipt: receipt}, nil
}

func firstMessageContent(opts *job.SendMessageOptions) string {
	if opts == nil || len(opts.Messages) == 0 || opts.Messages[0] == nil {
		return ""
	}
	return opts.Messages[0].Content
}

func (f *fakeJobService) ExecuteCommand(_ context.Context, _ string, clientMessageID, payload string, execute func() *model.CommandSystemMessageEvent) (*model.CommandSystemMessageEvent, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if event, ok := f.commands[clientMessageID]; ok {
		if f.commandPayloads[clientMessageID] != payload {
			return nil, false, job.ErrClientMessageIDConflict
		}
		return event, true, nil
	}
	f.commandCalls++
	event := execute()
	if f.commandErr != nil {
		return nil, false, f.commandErr
	}
	if f.commands == nil {
		f.commands = make(map[string]*model.CommandSystemMessageEvent)
		f.commandPayloads = make(map[string]string)
	}
	stored := *event
	f.commands[clientMessageID] = &stored
	f.commandPayloads[clientMessageID] = payload
	return event, false, nil
}

func (f *fakeJobService) PublishTransient(string, any) {
	f.mu.Lock()
	f.publishCalls++
	f.mu.Unlock()
}

// A message sent to a Running job is rejected by SendMessage with
// ErrJobRunning (409). prepareJobSend must reject it UP FRONT: the pre-send
// flow applies metadata side effects (job title from the first message,
// session model / ACP field updates) that must not land for a send that is
// about to be rejected — otherwise the failed send's selection leaks into
// the next successful run.
func TestPrepareJobSend_RunningJobRejectedBeforeSideEffects(t *testing.T) {
	fjs := &fakeJobService{}
	h := &Handler{jobService: fjs, settingsService: &fakeSettings{}}
	// Default title + first user message: the buggy path would push a title
	// update before SendMessage rejected the send.
	j := &model.Job{ID: "j1", WorkspaceID: "ws1", Status: model.JobStatusRunning}
	req := &model.JobMessageRequest{Messages: []model.RequestMessage{{Content: "hello"}}}

	runner, opts, err := h.prepareJobSend(context.Background(), j, req)
	if !errors.Is(err, job.ErrJobRunning) {
		t.Fatalf("expected ErrJobRunning for a Running job, got err=%v runner=%v opts=%v", err, runner, opts)
	}
	if runner != nil || opts != nil {
		t.Errorf("rejected send must not produce runner/opts, got runner=%v opts=%v", runner, opts)
	}
	if n := fjs.titleCallCount(); n != 0 {
		t.Errorf("rejected send must not apply title side effects, got %d UpdateTitle calls", n)
	}
}

// Same gate for a soft-deleted job: SendMessage would reject with
// ErrJobDeleted, so prepareJobSend must not touch metadata either.
func TestPrepareJobSend_DeletedJobRejectedBeforeSideEffects(t *testing.T) {
	fjs := &fakeJobService{}
	h := &Handler{jobService: fjs, settingsService: &fakeSettings{}}
	j := &model.Job{ID: "j2", WorkspaceID: "ws1", Deleted: true}
	req := &model.JobMessageRequest{Messages: []model.RequestMessage{{Content: "hello"}}}

	_, _, err := h.prepareJobSend(context.Background(), j, req)
	if !errors.Is(err, job.ErrJobDeleted) {
		t.Fatalf("expected ErrJobDeleted for a deleted job, got %v", err)
	}
	if n := fjs.titleCallCount(); n != 0 {
		t.Errorf("rejected send must not apply title side effects, got %d UpdateTitle calls", n)
	}
}

// Happy path: preparation itself stays side-effect-free; the title update is
// attached to the runner and runs only after SendMessage wins its claim.
func TestPrepareJobSend_DefersSideEffectsUntilAccepted(t *testing.T) {
	fjs := &fakeJobService{}
	h := &Handler{jobService: fjs, settingsService: &fakeSettings{}}
	j := &model.Job{ID: "j3", WorkspaceID: "ws1", Status: model.JobStatusCompleted}
	req := &model.JobMessageRequest{Messages: []model.RequestMessage{{Content: "hello"}}}

	runner, opts, err := h.prepareJobSend(context.Background(), j, req)
	if err != nil {
		t.Fatalf("prepareJobSend failed for a sendable job: %v", err)
	}
	if runner == nil || opts == nil {
		t.Fatal("expected runner and opts for a sendable job")
	}
	if n := fjs.titleCallCount(); n != 0 {
		t.Errorf("prepare must not apply title side effects, got %d calls", n)
	}
	preparer, ok := runner.(job.AcceptedMessagePreparer)
	if !ok {
		t.Fatalf("runner %T does not implement AcceptedMessagePreparer", runner)
	}
	if err := preparer.PrepareAcceptedMessage(context.Background(), j.ID); err != nil {
		t.Fatalf("PrepareAcceptedMessage: %v", err)
	}
	if n := fjs.titleCallCount(); n != 1 {
		t.Errorf("accepted message should apply title once, got %d calls", n)
	}
}

func TestJobMessage_RetrySameClientMessageIDAcknowledgesDuplicate(t *testing.T) {
	fjs := &fakeJobService{
		job: &model.Job{
			ID:          "job-1",
			WorkspaceID: "ws-1",
			Title:       "existing title",
			Status:      model.JobStatusCompleted,
		},
		receipts: make(map[string]job.MessageReceipt),
	}
	h := &Handler{jobService: fjs}
	engine := route.NewEngine(config.NewOptions(nil))
	engine.POST("/api/v1/job/:jobId/message", h.JobMessage)
	body := []byte(`{
		"clientMessageId":"client-1",
		"messages":[{"id":"client-1","type":"text","role":"user","content":"hello"}]
	}`)

	perform := func() map[string]any {
		recorder := ut.PerformRequest(
			engine,
			http.MethodPost,
			"/api/v1/job/job-1/message",
			&ut.Body{Body: bytes.NewReader(body), Len: len(body)},
			ut.Header{Key: "Content-Type", Value: "application/json"},
		)
		resp := recorder.Result()
		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode(), resp.Body())
		}
		var decoded map[string]any
		if err := json.Unmarshal(resp.Body(), &decoded); err != nil {
			t.Fatalf("decode response: %v body=%s", err, resp.Body())
		}
		return decoded
	}

	first := perform()
	second := perform()
	if first["status"] != string(job.SendMessageStarted) {
		t.Fatalf("first status=%v, want started", first["status"])
	}
	if second["status"] != string(job.SendMessageDuplicate) {
		t.Fatalf("retry status=%v, want duplicate", second["status"])
	}
	if second["messageState"] != string(model.ClientMessageStateProcessing) {
		t.Fatalf("retry messageState=%v, want processing", second["messageState"])
	}

	fjs.mu.Lock()
	defer fjs.mu.Unlock()
	if fjs.startCalls != 1 {
		t.Fatalf("Agent starts=%d, want 1", fjs.startCalls)
	}
	if fjs.sendCalls != 1 {
		t.Fatalf("SendMessage calls=%d, want 1; retry should use durable lookup fast path", fjs.sendCalls)
	}
}

func TestJobMessage_CommandRetryReturnsStoredResultWithoutRedispatch(t *testing.T) {
	fjs := &fakeJobService{
		job: &model.Job{ID: "job-command", WorkspaceID: "ws-1", Title: "existing title"},
	}
	h := &Handler{
		jobService: fjs,
		workspaceService: &createJobWorkspaceService{workspace: &model.Workspace{
			ID: "ws-1", Workdir: t.TempDir(),
		}},
	}
	engine := route.NewEngine(config.NewOptions(nil))
	engine.POST("/api/v1/job/:jobId/message", h.JobMessage)
	body := []byte(`{"clientMessageId":"command-1","messages":[{"role":"user","content":"/new"}]}`)

	perform := func() map[string]any {
		recorder := ut.PerformRequest(engine, http.MethodPost, "/api/v1/job/job-command/message",
			&ut.Body{Body: bytes.NewReader(body), Len: len(body)},
			ut.Header{Key: "Content-Type", Value: "application/json"})
		resp := recorder.Result()
		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode(), resp.Body())
		}
		var decoded map[string]any
		if err := json.Unmarshal(resp.Body(), &decoded); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return decoded
	}

	first := perform()
	second := perform()
	if first["status"] != "command_dispatched" || second["status"] != "command_duplicate" {
		t.Fatalf("statuses=(%v,%v), want command_dispatched/command_duplicate", first["status"], second["status"])
	}
	firstEvent := first["event"].(map[string]any)
	secondEvent := second["event"].(map[string]any)
	firstAction := firstEvent["action"].(map[string]any)
	secondAction := secondEvent["action"].(map[string]any)
	if firstAction["clientMessageId"] == "" || firstAction["clientMessageId"] != secondAction["clientMessageId"] {
		t.Fatalf("command action keys=(%v,%v), want same non-empty key", firstAction["clientMessageId"], secondAction["clientMessageId"])
	}
	fjs.mu.Lock()
	defer fjs.mu.Unlock()
	if fjs.commandCalls != 1 {
		t.Fatalf("command executions=%d, want 1", fjs.commandCalls)
	}
	if fjs.publishCalls != 1 {
		t.Fatalf("transient publishes=%d, want first dispatch only", fjs.publishCalls)
	}
}

func TestJobMessage_CommandReceiptFailureDoesNotPublishAction(t *testing.T) {
	fjs := &fakeJobService{
		job:        &model.Job{ID: "job-command-fail", WorkspaceID: "ws-1", Title: "existing title"},
		commandErr: errors.New("persist command receipt failed"),
	}
	h := &Handler{jobService: fjs}
	engine := route.NewEngine(config.NewOptions(nil))
	engine.POST("/api/v1/job/:jobId/message", h.JobMessage)
	body := []byte(`{"clientMessageId":"command-fail","messages":[{"role":"user","content":"/help"}]}`)
	recorder := ut.PerformRequest(engine, http.MethodPost, "/api/v1/job/job-command-fail/message",
		&ut.Body{Body: bytes.NewReader(body), Len: len(body)}, ut.Header{Key: "Content-Type", Value: "application/json"})
	if recorder.Result().StatusCode() != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500", recorder.Result().StatusCode(), recorder.Result().Body())
	}
	fjs.mu.Lock()
	defer fjs.mu.Unlock()
	if fjs.commandCalls != 1 || fjs.publishCalls != 0 {
		t.Fatalf("commandCalls/publishCalls=%d/%d, want 1/0", fjs.commandCalls, fjs.publishCalls)
	}
}

func TestJobMessage_ConcurrentDifferentPayloadLoserHasNoPreClaimSideEffects(t *testing.T) {
	fjs := &fakeJobService{
		job: &model.Job{ID: "job-conflict", WorkspaceID: "ws-1", Status: model.JobStatusCompleted},
	}
	h := &Handler{jobService: fjs, settingsService: &fakeSettings{}}
	firstReq := &model.JobMessageRequest{
		ClientMessageID: "shared-id",
		Messages:        []model.RequestMessage{{Content: "winning payload"}},
	}
	runner, opts, err := h.prepareJobSend(context.Background(), fjs.job.DeepCopy(), firstReq)
	if err != nil {
		t.Fatalf("prepare winner: %v", err)
	}
	first, err := fjs.SendMessage(context.Background(), fjs.job.ID, runner, opts)
	if err != nil || !first.Started() {
		t.Fatalf("winner send=(%#v,%v), want started", first, err)
	}

	losingReq := &model.JobMessageRequest{
		ClientMessageID: "shared-id",
		Messages:        []model.RequestMessage{{Content: "losing payload"}},
	}
	// This mirrors the request-race window: the stale snapshot still looks
	// sendable, so preparation runs before the service's authoritative claim.
	loserRunner, loserOpts, err := h.prepareJobSend(context.Background(), fjs.job.DeepCopy(), losingReq)
	if err != nil {
		t.Fatalf("prepare loser: %v", err)
	}
	if n := fjs.titleCallCount(); n != 1 {
		t.Fatalf("only winner should change title before loser claim: %d calls", n)
	}
	_, err = fjs.SendMessage(context.Background(), fjs.job.ID, loserRunner, loserOpts)
	if !errors.Is(err, job.ErrClientMessageIDConflict) {
		t.Fatalf("loser error=%v, want clientMessageId conflict", err)
	}
	if n := fjs.titleCallCount(); n != 1 {
		t.Fatalf("losing conflict changed title: %d calls", n)
	}
	fjs.mu.Lock()
	defer fjs.mu.Unlock()
	if fjs.prepareCalls != 1 {
		t.Fatalf("accepted preparation calls=%d, want winner only", fjs.prepareCalls)
	}
}
