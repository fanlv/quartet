package job

import (
	"context"
	"errors"
	"io"
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

func TestTransientNetworkErrorEOFMatchingIsPrecise(t *testing.T) {
	if !isTransientNetworkError(io.EOF) {
		t.Fatal("io.EOF should be transient")
	}
	if !isTransientNetworkError(errors.New("stream read failed: unexpected EOF")) {
		t.Fatal("unexpected EOF should be transient")
	}
	if isTransientNetworkError(errors.New("failed to parse EOF marker in model output")) {
		t.Fatal("unrelated EOF text should not be transient")
	}
}

func TestRateLimitErrorMatchesCommon429Shapes(t *testing.T) {
	for _, msg := range []string{
		"status 429",
		"status_code: 429",
		"status code 429",
		"StatusCode: 429",
		"HTTP 429",
		"code 429",
		"status=429",
		"Too Many Requests",
	} {
		t.Run(msg, func(t *testing.T) {
			if !isRateLimitError(errors.New(msg)) {
				t.Fatalf("%q should be rate-limit", msg)
			}
		})
	}
	if isRateLimitError(errors.New("business error mentions number 4290")) {
		t.Fatal("unrelated numbers should not be rate-limit")
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

func TestRunStartedTimestampInteractiveUsesJobStartedAt(t *testing.T) {
	jobID := "job-test"
	s := newStateTestService()
	reader, err := s.Subscribe(jobID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	job := &model.Job{ID: jobID, StartedAt: 123456789, Progress: &model.JobProgress{}}
	s.executeAgentTurn(context.Background(), job, stubRunner{}, "hi", "sess", nil)

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

// TestInteractiveRunPublishesRunEnd asserts the round opened by RUN_STARTED
// on an interactive (`SendMessage`) run is closed by RUN_FINISHED on success /
// RUN_ERROR on error. Without this, the buffer's openRoundID stays non-empty,
// SnapshotSeq returns (round_start - 1), and reconnecting clients re-receive
// the round's chunks even though messages.jsonl already covers them.
func TestInteractiveRunPublishesRunEnd(t *testing.T) {
	tests := []struct {
		name       string
		runner     JobRunner
		wantClosed bool // round must be closed (openRoundID == "")
		wantOK     bool // RUN_FINISHED if true, else RUN_ERROR
	}{
		{name: "success closes round with RUN_FINISHED", runner: stubRunner{}, wantClosed: true, wantOK: true},
		{name: "failure closes round with RUN_ERROR", runner: errStubRunner{}, wantClosed: true, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobID := "job-run-end-" + tt.name
			s := newStateTestService()
			reader, err := s.Subscribe(jobID, 0)
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			defer reader.Close()

			job := &model.Job{ID: jobID, StartedAt: 1, Progress: &model.JobProgress{}}
			s.executeAgentTurn(context.Background(), job, tt.runner, "hi", "sess", nil)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			var sawFinished, sawError bool
		drain:
			for {
				entries, ok := reader.Read(ctx, 16)
				if !ok {
					break drain
				}
				for _, entry := range entries {
					switch entry.Event.(type) {
					case *model.RunFinishedEvent:
						sawFinished = true
					case *model.RunErrorEvent:
						sawError = true
					}
					if entry.Seq > 0 {
						reader.Ack(entry.Seq)
					}
				}
				if sawFinished || sawError {
					break drain
				}
			}

			if tt.wantOK && !sawFinished {
				t.Fatalf("expected RUN_FINISHED, got finished=%v error=%v", sawFinished, sawError)
			}
			if !tt.wantOK && !sawError {
				t.Fatalf("expected RUN_ERROR, got finished=%v error=%v", sawFinished, sawError)
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
				t.Fatalf("expected openRoundID empty after run end, got %q", openRound)
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
// interrupted path still closes the buffer round: without this, ResumeGC on
// the next SendMessage can never reclaim the orphan round's A-class chunks.
type cancelStubRunner struct{}

func (r cancelStubRunner) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	return "", nil
}

func (r cancelStubRunner) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	return context.Canceled
}

func (r cancelStubRunner) SessionModelID(sessionID string) string { return "" }

// TestInterruptedRunClosesBufferRound asserts that an interrupted run
// (RunIteration returns context.Canceled / DeadlineExceeded) still publishes
// a run-end event so the buffer's openRoundID is cleared. Before the fix,
// executeAgentTurn's isInterruptedRun branch returned early, leaving openRoundID
// set — the next run's ResumeGC then failed to reclaim the orphan round
// forever (round.closed stayed false, gc condition never met).
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
	s.executeAgentTurn(context.Background(), job, cancelStubRunner{}, "hi", "sess", nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var sawRunEnd bool
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
			}
			if entry.Seq > 0 {
				reader.Ack(entry.Seq)
			}
		}
		if sawRunEnd {
			break drain
		}
	}

	if !sawRunEnd {
		t.Fatal("expected a run-end event (RUN_FINISHED/RUN_ERROR) after interrupted run")
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

// TestInterruptedThenSendReclaimsOrphanRound is the end-to-end regression
// for the orphan-round leak: after an interrupted run + MarkTerminal +
// ResumeGC + a fresh round, the original round's A-class chunks must reclaim
// once cursors cross. Before the fix the orphan round had closed=false and
// survived forever.
func TestInterruptedThenSendReclaimsOrphanRound(t *testing.T) {
	jobID := "job-interrupt-continue"
	s := newStateTestService()
	reader, err := s.Subscribe(jobID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	job := &model.Job{ID: jobID, StartedAt: 1, Progress: &model.JobProgress{}}

	// Run #1: interrupted. Publishes RunStarted + RunFinished (cancel is not
	// an error — see publishRunOutcome).
	s.executeAgentTurn(context.Background(), job, cancelStubRunner{}, "hi", "sess", nil)

	// Simulate the terminal transition (Stop → JOB_STOPPED → MarkTerminal),
	// then the next SendMessage (ResumeGC + new run).
	buf := s.bus.get(jobID)
	if buf == nil {
		t.Fatal("buffer missing")
	}
	buf.MarkTerminal()
	buf.ResumeGC()

	// Run #2: succeeds.
	s.executeAgentTurn(context.Background(), job, stubRunner{}, "hi", "sess", nil)

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
