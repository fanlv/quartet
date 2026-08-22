package job

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

type idempotencyTestRepo struct {
	mu            sync.Mutex
	jobs          map[string]*model.Job
	saveCalls     int
	failNextSaves int
	failErr       error
}

var _ repository.JobRepo = (*idempotencyTestRepo)(nil)

func newIdempotencyTestRepo() *idempotencyTestRepo {
	return &idempotencyTestRepo{jobs: make(map[string]*model.Job)}
}

func (r *idempotencyTestRepo) Save(jobID string, job *model.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saveCalls++
	if r.failNextSaves > 0 {
		r.failNextSaves--
		return r.failErr
	}
	r.jobs[jobID] = job.DeepCopy()
	return nil
}

func (r *idempotencyTestRepo) Load(jobID string) (*model.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[jobID]
	if job == nil {
		return nil, nil
	}
	return job.DeepCopy(), nil
}

func (r *idempotencyTestRepo) ListIDs() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.jobs))
	for id := range r.jobs {
		ids = append(ids, id)
	}
	return ids, nil
}

func (r *idempotencyTestRepo) LoadAll() ([]*model.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	jobs := make([]*model.Job, 0, len(r.jobs))
	for _, job := range r.jobs {
		jobs = append(jobs, job.DeepCopy())
	}
	return jobs, nil
}

func (r *idempotencyTestRepo) seed(job *model.Job) {
	r.mu.Lock()
	r.jobs[job.ID] = job.DeepCopy()
	r.mu.Unlock()
}

func (r *idempotencyTestRepo) failNext(count int, err error) {
	r.mu.Lock()
	r.failNextSaves = count
	r.failErr = err
	r.mu.Unlock()
}

func (r *idempotencyTestRepo) saved(jobID string) (*model.Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[jobID]
	if !ok {
		return nil, false
	}
	return job.DeepCopy(), true
}

func (r *idempotencyTestRepo) saves() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saveCalls
}

type idempotencyTestRunner struct {
	calls     atomic.Int32
	started   chan struct{}
	startOnce sync.Once
	release   <-chan struct{}
	runErr    error
}

type preparedIdempotencyRunner struct {
	*idempotencyTestRunner
	prepareCalls atomic.Int32
	releaseCalls atomic.Int32
}

func (r *preparedIdempotencyRunner) PrepareAcceptedMessage(context.Context, string) error {
	r.prepareCalls.Add(1)
	return nil
}

func (r *preparedIdempotencyRunner) ReleasePreparedExecution() {
	r.releaseCalls.Add(1)
}

func newIdempotencyTestRunner(release <-chan struct{}, runErr error) *idempotencyTestRunner {
	return &idempotencyTestRunner{
		started: make(chan struct{}),
		release: release,
		runErr:  runErr,
	}
}

func (r *idempotencyTestRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-idempotency", nil
}

func (r *idempotencyTestRunner) RunIteration(ctx context.Context, _ string, _ []*schema.Message, _ agui.EventHandler) error {
	r.calls.Add(1)
	r.startOnce.Do(func() { close(r.started) })
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.runErr
}

func (r *idempotencyTestRunner) SessionModelID(string) string { return "" }

func (r *idempotencyTestRunner) callCount() int {
	return int(r.calls.Load())
}

type idempotencyFixedClock int64

func (c idempotencyFixedClock) NowMillis() int64 { return int64(c) }

func newIdempotencyService(repo repository.JobRepo) *serviceImpl {
	svc := newStateTestService()
	svc.repos = map[string]repository.JobRepo{"": repo}
	return svc
}

func addIdempotencyJob(svc *serviceImpl, jobID string) *model.Job {
	job := &model.Job{
		ID:          jobID,
		WorkspaceID: "",
		Mode:        model.JobModeInteractive,
		Status:      model.JobStatusPending,
		SessionIDs:  []string{"session-idempotency"},
		Progress:    &model.JobProgress{},
	}
	svc.store(job.ID, job)
	return job
}

func idempotencyTestOptions(clientMessageID, content string) *SendMessageOptions {
	return &SendMessageOptions{
		SessionID:            "session-idempotency",
		IdempotencySessionID: "session-idempotency",
		ClientMessageID:      clientMessageID,
		Messages:             []*schema.Message{schema.UserMessage(content)},
	}
}

func waitForIdempotencyRunner(t *testing.T, runner *idempotencyTestRunner) {
	t.Helper()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RunIteration")
	}
}

func waitForIdempotencyReceipt(t *testing.T, svc *serviceImpl, jobID string, opts *SendMessageOptions, want model.ClientMessageState) MessageReceipt {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		receipt, found, err := svc.LookupMessage(jobID, opts)
		if err != nil {
			t.Fatalf("LookupMessage: %v", err)
		}
		if found && receipt.State == want {
			return receipt
		}
		if time.Now().After(deadline) {
			if !found {
				t.Fatalf("timed out waiting for receipt state %q: receipt not found", want)
			}
			t.Fatalf("timed out waiting for receipt state %q: got %q", want, receipt.State)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForPersistedIdempotencyReceipt(t *testing.T, repo *idempotencyTestRepo, jobID, clientMessageID string, want model.ClientMessageState) model.ClientMessageReceipt {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, found := repo.saved(jobID)
		if found {
			receipt, exists := job.ClientMessageReceipts[clientMessageID]
			if exists && receipt.State == want {
				return receipt
			}
		}
		if time.Now().After(deadline) {
			if !found {
				t.Fatalf("timed out waiting for persisted receipt state %q: job not found", want)
			}
			receipt, exists := job.ClientMessageReceipts[clientMessageID]
			if !exists {
				t.Fatalf("timed out waiting for persisted receipt state %q: receipt not found", want)
			}
			t.Fatalf("timed out waiting for persisted receipt state %q: got %q", want, receipt.State)
		}
		time.Sleep(time.Millisecond)
	}
}

func idempotencyJobIDFromOptions(opts *SendMessageOptions) string {
	return "job-" + opts.ClientMessageID
}

func TestSendMessageClientMessageIDConcurrentClaimRunsOnce(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	opts := idempotencyTestOptions("concurrent", "hello")
	jobID := idempotencyJobIDFromOptions(opts)
	addIdempotencyJob(svc, jobID)

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRunner := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRunner()
	runner := newIdempotencyTestRunner(release, nil)

	type outcome struct {
		result SendMessageResult
		err    error
	}
	ready := sync.WaitGroup{}
	ready.Add(2)
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ready.Done()
			<-start
			result, err := svc.SendMessage(context.Background(), jobID, runner, opts)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	ready.Wait()
	close(start)

	counts := map[SendMessageDisposition]int{}
	for i := 0; i < 2; i++ {
		select {
		case got := <-outcomes:
			if got.err != nil {
				t.Fatalf("concurrent SendMessage returned error: %v", got.err)
			}
			counts[got.result.Disposition]++
			if got.result.Receipt.ClientMessageID != opts.ClientMessageID {
				t.Errorf("receipt clientMessageId = %q, want %q", got.result.Receipt.ClientMessageID, opts.ClientMessageID)
			}
			if got.result.Receipt.State != model.ClientMessageStateProcessing {
				t.Errorf("receipt state = %q, want processing", got.result.Receipt.State)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent SendMessage")
		}
	}
	if counts[SendMessageStarted] != 1 || counts[SendMessageDuplicate] != 1 {
		t.Fatalf("dispositions = %#v, want one started and one duplicate", counts)
	}
	waitForIdempotencyRunner(t, runner)
	if got := runner.callCount(); got != 1 {
		t.Fatalf("RunIteration calls = %d, want 1", got)
	}
	persisted, ok := repo.saved(jobID)
	if !ok {
		t.Fatal("processing claim was not persisted")
	}
	if persisted.ActiveClientMessageID != opts.ClientMessageID || persisted.ClientMessageReceipts[opts.ClientMessageID].State != model.ClientMessageStateProcessing {
		t.Fatalf("persisted claim = active %q receipt %#v, want active processing claim", persisted.ActiveClientMessageID, persisted.ClientMessageReceipts[opts.ClientMessageID])
	}

	releaseRunner()
	waitForIdempotencyReceipt(t, svc, jobID, opts, model.ClientMessageStateCompleted)
	waitForPersistedIdempotencyReceipt(t, repo, jobID, opts.ClientMessageID, model.ClientMessageStateCompleted)
}

func TestSendMessageClientMessageIDDuplicateAfterCompletionDoesNotRun(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	opts := idempotencyTestOptions("completed", "hello")
	jobID := idempotencyJobIDFromOptions(opts)
	addIdempotencyJob(svc, jobID)
	runner := newIdempotencyTestRunner(nil, nil)

	first, err := svc.SendMessage(context.Background(), jobID, runner, opts)
	if err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}
	if first.Disposition != SendMessageStarted {
		t.Fatalf("first disposition = %q, want started", first.Disposition)
	}
	completed := waitForIdempotencyReceipt(t, svc, jobID, opts, model.ClientMessageStateCompleted)
	if completed.FinishedAt <= 0 {
		t.Fatalf("completed receipt FinishedAt = %d, want > 0", completed.FinishedAt)
	}
	persistedCompleted := waitForPersistedIdempotencyReceipt(t, repo, jobID, opts.ClientMessageID, model.ClientMessageStateCompleted)
	if persistedCompleted.FinishedAt != completed.FinishedAt {
		t.Fatalf("persisted FinishedAt = %d, want lookup FinishedAt %d", persistedCompleted.FinishedAt, completed.FinishedAt)
	}
	savesBeforeDuplicate := repo.saves()

	duplicate, err := svc.SendMessage(context.Background(), jobID, runner, opts)
	if err != nil {
		t.Fatalf("duplicate SendMessage: %v", err)
	}
	if duplicate.Disposition != SendMessageDuplicate || duplicate.Receipt.State != model.ClientMessageStateCompleted {
		t.Fatalf("duplicate result = %#v, want duplicate/completed", duplicate)
	}
	if got := runner.callCount(); got != 1 {
		t.Fatalf("RunIteration calls = %d, want 1", got)
	}
	if got := repo.saves(); got != savesBeforeDuplicate {
		t.Fatalf("Save calls after completed duplicate = %d, want unchanged %d", got, savesBeforeDuplicate)
	}
}

func TestSendMessageClientMessageIDDuplicateAfterFailureDoesNotRun(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	opts := idempotencyTestOptions("failed", "hello")
	jobID := idempotencyJobIDFromOptions(opts)
	addIdempotencyJob(svc, jobID)
	runErr := errors.New("agent failed")
	runner := newIdempotencyTestRunner(nil, runErr)

	first, err := svc.SendMessage(context.Background(), jobID, runner, opts)
	if err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}
	if first.Disposition != SendMessageStarted {
		t.Fatalf("first disposition = %q, want started", first.Disposition)
	}
	failed := waitForIdempotencyReceipt(t, svc, jobID, opts, model.ClientMessageStateFailed)
	if failed.FinishedAt <= 0 {
		t.Fatalf("failed receipt FinishedAt = %d, want > 0", failed.FinishedAt)
	}
	persistedFailed := waitForPersistedIdempotencyReceipt(t, repo, jobID, opts.ClientMessageID, model.ClientMessageStateFailed)
	if persistedFailed.FinishedAt != failed.FinishedAt {
		t.Fatalf("persisted FinishedAt = %d, want lookup FinishedAt %d", persistedFailed.FinishedAt, failed.FinishedAt)
	}
	savesBeforeDuplicate := repo.saves()

	duplicate, err := svc.SendMessage(context.Background(), jobID, runner, opts)
	if err != nil {
		t.Fatalf("duplicate SendMessage: %v", err)
	}
	if duplicate.Disposition != SendMessageDuplicate || duplicate.Receipt.State != model.ClientMessageStateFailed {
		t.Fatalf("duplicate result = %#v, want duplicate/failed", duplicate)
	}
	if got := runner.callCount(); got != 1 {
		t.Fatalf("RunIteration calls = %d, want 1", got)
	}
	if got := repo.saves(); got != savesBeforeDuplicate {
		t.Fatalf("Save calls after failed duplicate = %d, want unchanged %d", got, savesBeforeDuplicate)
	}
}

func TestSendMessageClientMessageIDRejectsDifferentPayload(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	opts := idempotencyTestOptions("conflict", "first payload")
	jobID := idempotencyJobIDFromOptions(opts)
	addIdempotencyJob(svc, jobID)

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRunner := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRunner()
	runner := newIdempotencyTestRunner(release, nil)
	first, err := svc.SendMessage(context.Background(), jobID, runner, opts)
	if err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}
	if first.Disposition != SendMessageStarted {
		t.Fatalf("first disposition = %q, want started", first.Disposition)
	}
	waitForIdempotencyRunner(t, runner)

	conflicting := idempotencyTestOptions(opts.ClientMessageID, "different payload")
	if _, err := svc.SendMessage(context.Background(), jobID, runner, conflicting); !errors.Is(err, ErrClientMessageIDConflict) {
		t.Fatalf("conflicting SendMessage error = %v, want ErrClientMessageIDConflict", err)
	}
	if _, _, err := svc.LookupMessage(jobID, conflicting); !errors.Is(err, ErrClientMessageIDConflict) {
		t.Fatalf("conflicting LookupMessage error = %v, want ErrClientMessageIDConflict", err)
	}
	if got := runner.callCount(); got != 1 {
		t.Fatalf("RunIteration calls = %d after conflict, want 1", got)
	}

	releaseRunner()
	waitForIdempotencyReceipt(t, svc, jobID, opts, model.ClientMessageStateCompleted)
}

func TestSendMessageConflictingPayloadDoesNotRunAcceptedPreparationAndReleasesLease(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	opts := idempotencyTestOptions("claim-conflict", "winner")
	jobID := idempotencyJobIDFromOptions(opts)
	addIdempotencyJob(svc, jobID)
	winner := &preparedIdempotencyRunner{idempotencyTestRunner: newIdempotencyTestRunner(nil, nil)}
	if _, err := svc.SendMessage(context.Background(), jobID, winner, opts); err != nil {
		t.Fatalf("winner SendMessage: %v", err)
	}
	waitForIdempotencyReceipt(t, svc, jobID, opts, model.ClientMessageStateCompleted)
	if got := winner.prepareCalls.Load(); got != 1 {
		t.Fatalf("winner prepare calls=%d, want 1", got)
	}

	loser := &preparedIdempotencyRunner{idempotencyTestRunner: newIdempotencyTestRunner(nil, nil)}
	conflicting := idempotencyTestOptions(opts.ClientMessageID, "loser")
	if _, err := svc.SendMessage(context.Background(), jobID, loser, conflicting); !errors.Is(err, ErrClientMessageIDConflict) {
		t.Fatalf("loser error=%v, want conflict", err)
	}
	if got := loser.prepareCalls.Load(); got != 0 {
		t.Fatalf("loser prepare calls=%d, want 0", got)
	}
	if got := loser.callCount(); got != 0 {
		t.Fatalf("loser Agent calls=%d, want 0", got)
	}
	if got := loser.releaseCalls.Load(); got != 1 {
		t.Fatalf("loser release calls=%d, want 1", got)
	}
}

func TestClientMessageIDStartupRecoveryInterruptsReceiptAndDeduplicates(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	const interruptedAt int64 = 1700000000123
	svc.clock = idempotencyFixedClock(interruptedAt)
	opts := idempotencyTestOptions("recovered", "hello")
	jobID := idempotencyJobIDFromOptions(opts)
	payloadHash, err := clientMessagePayloadHash(opts)
	if err != nil {
		t.Fatalf("clientMessagePayloadHash: %v", err)
	}
	job := &model.Job{
		ID:                    jobID,
		WorkspaceID:           "",
		Mode:                  model.JobModeInteractive,
		Status:                model.JobStatusRunning,
		SessionIDs:            []string{"session-idempotency"},
		Progress:              &model.JobProgress{},
		FirstModelID:          "model-id",
		ActiveClientMessageID: opts.ClientMessageID,
		ClientMessageReceipts: map[string]model.ClientMessageReceipt{
			opts.ClientMessageID: {
				State:       model.ClientMessageStateProcessing,
				PayloadHash: payloadHash,
				AcceptedAt:  interruptedAt - 100,
			},
		},
	}
	repo.seed(job)
	if !svc.reconcileLoadedJob(context.Background(), repo, job) {
		t.Fatal("reconcileLoadedJob unexpectedly skipped active job")
	}
	svc.store(job.ID, job)

	if job.Status != model.JobStatusFailed || job.ActiveClientMessageID != "" {
		t.Fatalf("recovered job status/active = %q/%q, want failed/empty", job.Status, job.ActiveClientMessageID)
	}
	recoveredReceipt := job.ClientMessageReceipts[opts.ClientMessageID]
	if recoveredReceipt.State != model.ClientMessageStateInterrupted || recoveredReceipt.FinishedAt != interruptedAt {
		t.Fatalf("recovered receipt = %#v, want interrupted at %d", recoveredReceipt, interruptedAt)
	}
	persisted, ok := repo.saved(jobID)
	if !ok || persisted.ClientMessageReceipts[opts.ClientMessageID].State != model.ClientMessageStateInterrupted {
		t.Fatalf("interrupted recovery was not persisted: %#v", persisted)
	}

	lookup, found, err := svc.LookupMessage(jobID, opts)
	if err != nil || !found {
		t.Fatalf("LookupMessage after recovery = (%#v, %t, %v), want found", lookup, found, err)
	}
	if lookup.State != model.ClientMessageStateInterrupted {
		t.Fatalf("lookup state = %q, want interrupted", lookup.State)
	}
	runner := newIdempotencyTestRunner(nil, nil)
	duplicate, err := svc.SendMessage(context.Background(), jobID, runner, opts)
	if err != nil {
		t.Fatalf("SendMessage after recovery: %v", err)
	}
	if duplicate.Disposition != SendMessageDuplicate || duplicate.Receipt.State != model.ClientMessageStateInterrupted {
		t.Fatalf("SendMessage after recovery = %#v, want duplicate/interrupted", duplicate)
	}
	if got := runner.callCount(); got != 0 {
		t.Fatalf("RunIteration calls after recovery = %d, want 0", got)
	}
}

func TestSendMessageClientMessageIDClaimSaveFailureRollsBackAndAllowsRetry(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	opts := idempotencyTestOptions("save-failure", "hello")
	jobID := idempotencyJobIDFromOptions(opts)
	job := addIdempotencyJob(svc, jobID)
	repo.seed(job)
	runner := newIdempotencyTestRunner(nil, nil)

	saveErr := errors.New("forced claim save failure")
	// saveJobWithRetry performs two attempts; fail both so SendMessage must
	// roll the processing claim back instead of launching the Agent.
	repo.failNext(2, saveErr)
	failedResult, err := svc.SendMessage(context.Background(), jobID, runner, opts)
	if !errors.Is(err, saveErr) {
		t.Fatalf("first SendMessage error = %v, want forced save error", err)
	}
	if failedResult != (SendMessageResult{}) {
		t.Fatalf("first result = %#v, want zero result", failedResult)
	}
	if got := repo.saves(); got != 2 {
		t.Fatalf("Save calls after failed claim = %d, want 2 attempts", got)
	}
	if got := runner.callCount(); got != 0 {
		t.Fatalf("RunIteration calls after failed claim = %d, want 0", got)
	}
	rolledBack, ok := svc.Get(jobID)
	if !ok {
		t.Fatal("job disappeared after claim rollback")
	}
	if rolledBack.Status != model.JobStatusPending || rolledBack.ActiveClientMessageID != "" {
		t.Fatalf("rolled-back status/active = %q/%q, want pending/empty", rolledBack.Status, rolledBack.ActiveClientMessageID)
	}
	if _, exists := rolledBack.ClientMessageReceipts[opts.ClientMessageID]; exists {
		t.Fatalf("failed claim receipt still present after rollback: %#v", rolledBack.ClientMessageReceipts[opts.ClientMessageID])
	}
	persisted, found := repo.saved(jobID)
	if !found {
		t.Fatal("previous persisted job disappeared after failed claim")
	}
	if persisted.Status != model.JobStatusPending || persisted.ActiveClientMessageID != "" {
		t.Fatalf("persisted status/active after failed claim = %q/%q, want pending/empty", persisted.Status, persisted.ActiveClientMessageID)
	}
	if _, exists := persisted.ClientMessageReceipts[opts.ClientMessageID]; exists {
		t.Fatalf("failed processing claim reached durable storage: %#v", persisted.ClientMessageReceipts[opts.ClientMessageID])
	}
	if receipt, found, lookupErr := svc.LookupMessage(jobID, opts); lookupErr != nil || found {
		t.Fatalf("LookupMessage after rollback = (%#v, %t, %v), want not found", receipt, found, lookupErr)
	}

	retry, err := svc.SendMessage(context.Background(), jobID, runner, opts)
	if err != nil {
		t.Fatalf("retry SendMessage: %v", err)
	}
	if retry.Disposition != SendMessageStarted {
		t.Fatalf("retry disposition = %q, want started", retry.Disposition)
	}
	waitForIdempotencyRunner(t, runner)
	waitForIdempotencyReceipt(t, svc, jobID, opts, model.ClientMessageStateCompleted)
	waitForPersistedIdempotencyReceipt(t, repo, jobID, opts.ClientMessageID, model.ClientMessageStateCompleted)
	if got := runner.callCount(); got != 1 {
		t.Fatalf("RunIteration calls after successful retry = %d, want 1", got)
	}
}
