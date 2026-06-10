package job

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

type stubRunner struct{}

func (r stubRunner) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	return "", nil
}

func (r stubRunner) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	return nil
}

func (r stubRunner) SessionModelID(sessionID string) string {
	return ""
}

func TestExtractSetVars(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]string
	}{
		{"empty", "", nil},
		{"no match", "hello world", nil},
		{"single var", "prefix <<SET_VAR:foo=bar>> suffix", map[string]string{"foo": "bar"}},
		{"multiple vars", "<<SET_VAR:a=1>> <<SET_VAR:b=2>>", map[string]string{"a": "1", "b": "2"}},
		{"var with spaces in value", "<<SET_VAR:key=hello world>>", map[string]string{"key": "hello world"}},
		{"duplicate key last wins", "<<SET_VAR:x=old>> <<SET_VAR:x=new>>", map[string]string{"x": "new"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSetVars(tt.content)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("length mismatch: got %v, want %v", got, tt.want)
				return
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestIsInterruptedRun(t *testing.T) {
	if isInterruptedRun(nil) {
		t.Error("nil error should not be interrupted")
	}
	if !isInterruptedRun(context.Canceled) {
		t.Error("context.Canceled should be interrupted")
	}
	if !isInterruptedRun(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded should be interrupted")
	}
}

func TestBuildProgress(t *testing.T) {
	cfg := &model.LoopConfig{
		Flow: []model.FlowNode{
			{Type: model.FlowNodeTypeStep, RepeatCount: 3},
			{Type: model.FlowNodeTypeStep, RepeatCount: 2},
		},
	}
	p := buildProgress(cfg)
	if p.TotalSteps != 5 {
		t.Errorf("expected 5 total steps, got %d", p.TotalSteps)
	}
}

func TestRunStartedTimestampInteractiveUsesJobStartedAt(t *testing.T) {
	jobID := "job-test"
	s := newStateTestService()
	reader, err := s.Subscribe(jobID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	job := &model.Job{ID: jobID, StartedAt: 123456789, Progress: &model.JobProgress{}}
	node := model.FlowNode{Type: model.FlowNodeTypeStep, Message: "hi", RepeatCount: 1}
	res := s.executeRepeat(context.Background(), job, stubRunner{}, node, []int{0}, "sess", nil, false /* isLoopRun */, nil)
	if res != stepCompleted {
		t.Fatalf("executeRepeat result=%v, want %v", res, stepCompleted)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		entries, ok := reader.Read(ctx, 16)
		if !ok {
			t.Fatal("timeout waiting for RunStartedEvent")
		}
		for _, entry := range entries {
			if rs, ok := entry.Event.(*model.RunStartedEvent); ok {
				if rs.Timestamp != job.StartedAt {
					t.Fatalf("RUN_STARTED.timestamp=%d, want job.StartedAt=%d", rs.Timestamp, job.StartedAt)
				}
				return
			}
			if entry.Seq > 0 {
				reader.Ack(entry.Seq)
			}
		}
	}
}

// TestInteractiveRunPublishesIterationEnd asserts the round opened by
// ITERATION_STARTED on an interactive (`SendMessage`) run is closed by
// ITERATION_COMPLETED on success / ITERATION_FAILED on error. Without
// this, the buffer's openRoundID stays non-empty, SnapshotSeq returns
// (round_start - 1), and reconnecting clients re-receive the round's
// chunks even though messages.jsonl already covers them.
func TestInteractiveRunPublishesIterationEnd(t *testing.T) {
	tests := []struct {
		name       string
		runner     JobRunner
		wantClosed bool // round must be closed (openRoundID == "")
		wantOK     bool // ITERATION_COMPLETED if true, else ITERATION_FAILED
	}{
		{name: "success closes round with ITERATION_COMPLETED", runner: stubRunner{}, wantClosed: true, wantOK: true},
		{name: "failure closes round with ITERATION_FAILED", runner: errStubRunner{}, wantClosed: true, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobID := "job-iter-end-" + tt.name
			s := newStateTestService()
			reader, err := s.Subscribe(jobID, 0)
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			defer reader.Close()

			job := &model.Job{ID: jobID, StartedAt: 1, Progress: &model.JobProgress{}}
			node := model.FlowNode{Type: model.FlowNodeTypeStep, Message: "hi", RepeatCount: 1}
			s.executeRepeat(context.Background(), job, tt.runner, node, []int{0}, "sess", nil, false /* isLoopRun */, nil)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			var sawCompleted, sawFailed bool
		drain:
			for {
				entries, ok := reader.Read(ctx, 16)
				if !ok {
					break drain
				}
				for _, entry := range entries {
					switch entry.Event.(type) {
					case *model.IterationCompletedEvent:
						sawCompleted = true
					case *model.IterationFailedEvent:
						sawFailed = true
					}
					if entry.Seq > 0 {
						reader.Ack(entry.Seq)
					}
				}
				if sawCompleted || sawFailed {
					break drain
				}
			}

			if tt.wantOK && !sawCompleted {
				t.Fatalf("expected ITERATION_COMPLETED, got completed=%v failed=%v", sawCompleted, sawFailed)
			}
			if !tt.wantOK && !sawFailed {
				t.Fatalf("expected ITERATION_FAILED, got completed=%v failed=%v", sawCompleted, sawFailed)
			}

			// Buffer-level check: the round must be closed so SnapshotSeq
			// returns the tail (no in-flight round).
			buf := s.bus.get(jobID)
			if buf == nil {
				t.Fatal("buffer missing")
			}
			buf.mu.Lock()
			openRound := buf.openRoundID
			buf.mu.Unlock()
			if tt.wantClosed && openRound != "" {
				t.Fatalf("expected openRoundID empty after iteration end, got %q", openRound)
			}
		})
	}
}

type errStubRunner struct{}

func (r errStubRunner) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	return "", nil
}

func (r errStubRunner) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	return errStubFailure
}

func (r errStubRunner) SessionModelID(sessionID string) string { return "" }

var errStubFailure = errStubError("stub failure")

type errStubError string

func (e errStubError) Error() string { return string(e) }

// cancelStubRunner returns context.Canceled, mirroring an iteration that was
// interrupted by Stop / a parent cancel mid-flight. Used to assert that the
// interrupted path still closes the buffer round (see Issue 1+2 in the
// 2026-05-14 audit: without this, ResumeGC on Continue can never reclaim the
// orphan round's A-class chunks).
type cancelStubRunner struct{}

func (r cancelStubRunner) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	return "", nil
}

func (r cancelStubRunner) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	return context.Canceled
}

func (r cancelStubRunner) SessionModelID(sessionID string) string { return "" }

// TestInterruptedRunClosesBufferRound asserts that an interrupted iteration
// (RunIteration returns context.Canceled / DeadlineExceeded) still publishes
// a run-end event + ITERATION_FAILED so the buffer's openRoundID is cleared.
// Before the fix, executeRepeat's isInterruptedRun branch returned early,
// leaving openRoundID set — Continue's ResumeGC then failed to reclaim the
// orphan round forever (round.closed stayed false, gc condition never met).
//
// User-initiated stop (context.Canceled) closes the round via RUN_FINISHED
// (not RUN_ERROR) so the frontend doesn't show a spurious error toast — see
// publishRunOutcome. The round-close invariant holds for either run-end event.
func TestInterruptedRunClosesBufferRound(t *testing.T) {
	jobID := "job-interrupted"
	s := newStateTestService()
	reader, err := s.Subscribe(jobID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	job := &model.Job{ID: jobID, StartedAt: 1, Progress: &model.JobProgress{}}
	node := model.FlowNode{Type: model.FlowNodeTypeStep, Message: "hi", RepeatCount: 1}
	res := s.executeRepeat(context.Background(), job, cancelStubRunner{}, node, []int{0}, "sess", nil, false /* isLoopRun */, nil)
	if res != stepAborted {
		t.Fatalf("executeRepeat result=%v, want stepAborted", res)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var sawRunEnd, sawIterFailed bool
drain:
	for {
		entries, ok := reader.Read(ctx, 16)
		if !ok {
			break drain
		}
		for _, entry := range entries {
			switch entry.Event.(type) {
			case *model.RunErrorEvent, *model.RunFinishedEvent:
				sawRunEnd = true
			case *model.IterationFailedEvent:
				sawIterFailed = true
			}
			if entry.Seq > 0 {
				reader.Ack(entry.Seq)
			}
		}
		if sawRunEnd && sawIterFailed {
			break drain
		}
	}

	if !sawRunEnd {
		t.Fatal("expected a run-end event (RUN_FINISHED/RUN_ERROR) after interrupted run")
	}
	if !sawIterFailed {
		t.Fatal("expected ITERATION_FAILED after interrupted run")
	}

	buf := s.bus.get(jobID)
	if buf == nil {
		t.Fatal("buffer missing")
	}
	buf.mu.Lock()
	openRound := buf.openRoundID
	buf.mu.Unlock()
	if openRound != "" {
		t.Fatalf("expected openRoundID empty after interrupted run closed the round, got %q", openRound)
	}
}

// TestInterruptedThenContinueReclaimsOrphanRound is the end-to-end regression
// for Issue 1: after an interrupted run + MarkTerminal + ResumeGC + a fresh
// round, the original round's A-class chunks must reclaim once cursors cross.
// Before the fix the orphan round had closed=false and survived forever.
func TestInterruptedThenContinueReclaimsOrphanRound(t *testing.T) {
	jobID := "job-interrupt-continue"
	s := newStateTestService()
	reader, err := s.Subscribe(jobID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	job := &model.Job{ID: jobID, StartedAt: 1, Progress: &model.JobProgress{}}
	node := model.FlowNode{Type: model.FlowNodeTypeStep, Message: "hi", RepeatCount: 1}

	// Run #1: interrupted. Publishes IterationStarted, RunStarted, RunError,
	// IterationFailed.
	if res := s.executeRepeat(context.Background(), job, cancelStubRunner{}, node, []int{0}, "sess", nil, false, nil); res != stepAborted {
		t.Fatalf("run #1 result=%v, want stepAborted", res)
	}

	// Simulate the terminal transition (Stop → JOB_STOPPED → MarkTerminal),
	// then Continue (ResumeGC + new run).
	buf := s.bus.get(jobID)
	if buf == nil {
		t.Fatal("buffer missing")
	}
	buf.MarkTerminal()
	buf.ResumeGC()

	// Run #2: succeeds. Different path so it's a separate iteration.
	if res := s.executeRepeat(context.Background(), job, stubRunner{}, node, []int{1}, "sess", nil, false, nil); res != stepCompleted {
		t.Fatalf("run #2 result=%v, want stepCompleted", res)
	}

	// Drain everything and ack so the cursor crosses both rounds' end events.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		buf.mu.Lock()
		caughtUp := reader.cursor >= buf.nextSeq
		buf.mu.Unlock()
		if caughtUp {
			break
		}
		entries, ok := reader.Read(ctx, 16)
		if !ok {
			t.Fatal("reader closed before catching up")
		}
		for _, entry := range entries {
			if entry.Seq > 0 {
				reader.Ack(entry.Seq)
			}
		}
	}

	buf.mu.Lock()
	bufLen := len(buf.events)
	openRound := buf.openRoundID
	buf.mu.Unlock()
	if openRound != "" {
		t.Fatalf("expected no open round after run #2 closed, got %q", openRound)
	}
	if bufLen != 0 {
		t.Fatalf("expected buffer empty after both rounds closed and acked, got %d events", bufLen)
	}
}
