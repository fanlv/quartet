package job

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

func newCreateIdempotencyJob(clientMessageID, payloadHash string) *model.Job {
	createdAt := time.UnixMilli(1700000000123).UTC()
	return &model.Job{
		ID:                      IdempotentJobID(clientMessageID),
		Title:                   "original title",
		CreatedAt:               createdAt,
		UpdatedAt:               createdAt,
		Status:                  model.JobStatusPending,
		Mode:                    model.JobModeInteractive,
		WorkspaceID:             "",
		Workdir:                 "/workspace",
		CreationClientMessageID: clientMessageID,
		CreationPayloadHash:     payloadHash,
	}
}

func TestIdempotentJobIDIsStable(t *testing.T) {
	const clientMessageID = "create-client-1"
	const want = "job-idem-1fc50f70fcbb506967d5a4b286b43f7d"

	if got := IdempotentJobID(clientMessageID); got != want {
		t.Fatalf("IdempotentJobID(%q) = %q, want %q", clientMessageID, got, want)
	}
	if got := IdempotentJobID(clientMessageID); got != want {
		t.Fatalf("repeated IdempotentJobID(%q) = %q, want %q", clientMessageID, got, want)
	}
	if got := IdempotentJobID("create-client-2"); got == want {
		t.Fatalf("IdempotentJobID returned the same ID %q for distinct client message IDs", got)
	}
}

func TestCreateIdempotentConcurrentSamePayloadPersistsOnce(t *testing.T) {
	const callers = 16
	const clientMessageID = "concurrent-create"
	const payloadHash = "payload-hash"

	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)

	type outcome struct {
		job       *model.Job
		duplicate bool
		err       error
	}
	ready := sync.WaitGroup{}
	ready.Add(callers)
	start := make(chan struct{})
	outcomes := make(chan outcome, callers)
	for i := 0; i < callers; i++ {
		caller := i
		go func() {
			// Each caller owns its input. CreateIdempotent initializes Progress, so
			// sharing one pointer here would make the test itself racy.
			input := newCreateIdempotencyJob(clientMessageID, payloadHash)
			// Non-idempotency fields can differ between retries (for example, when
			// server-side defaults or timestamps are regenerated). Every caller must
			// still receive the one Job that won persistence.
			input.Title = fmt.Sprintf("caller-%d", caller)
			ready.Done()
			<-start
			created, duplicate, err := svc.CreateIdempotent(input)
			outcomes <- outcome{job: created, duplicate: duplicate, err: err}
		}()
	}
	ready.Wait()
	close(start)

	results := make([]outcome, 0, callers)
	fresh := 0
	duplicates := 0
	for i := 0; i < callers; i++ {
		select {
		case got := <-outcomes:
			if got.err != nil {
				t.Fatalf("concurrent CreateIdempotent returned error: %v", got.err)
			}
			if got.job == nil {
				t.Fatal("concurrent CreateIdempotent returned a nil Job")
			}
			if got.duplicate {
				duplicates++
			} else {
				fresh++
			}
			results = append(results, got)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent CreateIdempotent")
		}
	}

	if fresh != 1 || duplicates != callers-1 {
		t.Fatalf("fresh/duplicate results = %d/%d, want 1/%d", fresh, duplicates, callers-1)
	}
	if got := repo.saves(); got != 1 {
		t.Fatalf("Save calls = %d, want 1", got)
	}
	ids, err := repo.ListIDs()
	if err != nil {
		t.Fatalf("ListIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != IdempotentJobID(clientMessageID) {
		t.Fatalf("persisted Job IDs = %#v, want only %q", ids, IdempotentJobID(clientMessageID))
	}
	persisted, ok := repo.saved(ids[0])
	if !ok {
		t.Fatalf("persisted Job %q not found", ids[0])
	}
	for i, got := range results {
		if !reflect.DeepEqual(got.job, persisted) {
			t.Errorf("result %d Job = %#v, want persisted Job %#v", i, got.job, persisted)
		}
	}
}

func TestCreateIdempotentRetryAfterRestartReturnsPersistedJob(t *testing.T) {
	repo := newIdempotencyTestRepo()
	firstService := newIdempotencyService(repo)
	firstInput := newCreateIdempotencyJob("restart-create", "payload-hash")

	created, duplicate, err := firstService.CreateIdempotent(firstInput)
	if err != nil {
		t.Fatalf("first CreateIdempotent: %v", err)
	}
	if duplicate {
		t.Fatal("first CreateIdempotent reported a duplicate")
	}
	if got := repo.saves(); got != 1 {
		t.Fatalf("Save calls after first create = %d, want 1", got)
	}

	// A new service instance has no in-memory jobs but shares durable storage.
	restartedService := newIdempotencyService(repo)
	if _, ok := restartedService.Get(firstInput.ID); ok {
		t.Fatalf("restarted service unexpectedly had Job %q in memory", firstInput.ID)
	}
	retryInput := newCreateIdempotencyJob("restart-create", "payload-hash")
	retryInput.Title = "retry must not replace original"
	retried, duplicate, err := restartedService.CreateIdempotent(retryInput)
	if err != nil {
		t.Fatalf("CreateIdempotent after restart: %v", err)
	}
	if !duplicate {
		t.Fatal("CreateIdempotent after restart did not report a duplicate")
	}
	if got := repo.saves(); got != 1 {
		t.Fatalf("Save calls after restart retry = %d, want unchanged 1", got)
	}
	if !reflect.DeepEqual(retried, created) {
		t.Fatalf("Job returned after restart = %#v, want original %#v", retried, created)
	}
	loaded, ok := restartedService.Get(firstInput.ID)
	if !ok {
		t.Fatalf("restarted service did not cache persisted Job %q", firstInput.ID)
	}
	if !reflect.DeepEqual(loaded, created) {
		t.Fatalf("restarted service Job = %#v, want original %#v", loaded, created)
	}
}

func TestCreateIdempotentRejectsDifferentCreationPayloadHash(t *testing.T) {
	tests := []struct {
		name    string
		restart bool
	}{
		{name: "in memory"},
		{name: "after restart", restart: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newIdempotencyTestRepo()
			svc := newIdempotencyService(repo)
			original := newCreateIdempotencyJob("conflicting-create", "first-payload-hash")
			if _, duplicate, err := svc.CreateIdempotent(original); err != nil || duplicate {
				t.Fatalf("first CreateIdempotent = duplicate %t, error %v; want fresh success", duplicate, err)
			}
			if tt.restart {
				svc = newIdempotencyService(repo)
			}

			conflicting := newCreateIdempotencyJob("conflicting-create", "different-payload-hash")
			got, duplicate, err := svc.CreateIdempotent(conflicting)
			if !errors.Is(err, ErrClientMessageIDConflict) {
				t.Fatalf("conflicting CreateIdempotent error = %v, want ErrClientMessageIDConflict", err)
			}
			if got != nil || duplicate {
				t.Fatalf("conflicting CreateIdempotent = (%#v, %t), want (nil, false)", got, duplicate)
			}
			if got := repo.saves(); got != 1 {
				t.Fatalf("Save calls after conflict = %d, want unchanged 1", got)
			}
			persisted, ok := repo.saved(original.ID)
			if !ok {
				t.Fatalf("original Job %q disappeared after conflict", original.ID)
			}
			if persisted.CreationPayloadHash != original.CreationPayloadHash {
				t.Fatalf("persisted payload hash = %q, want original %q", persisted.CreationPayloadHash, original.CreationPayloadHash)
			}
		})
	}
}

func TestCreateIdempotentSaveFailureDoesNotEnterMemory(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	input := newCreateIdempotencyJob("failed-create", "payload-hash")
	saveErr := errors.New("forced create save failure")
	repo.failNext(1, saveErr)

	created, duplicate, err := svc.CreateIdempotent(input)
	if !errors.Is(err, saveErr) {
		t.Fatalf("CreateIdempotent error = %v, want forced save error", err)
	}
	if created != nil || duplicate {
		t.Fatalf("failed CreateIdempotent = (%#v, %t), want (nil, false)", created, duplicate)
	}
	if got := repo.saves(); got != 1 {
		t.Fatalf("Save calls = %d, want 1", got)
	}
	if _, ok := repo.saved(input.ID); ok {
		t.Fatalf("failed create unexpectedly persisted Job %q", input.ID)
	}
	if got, ok := svc.Get(input.ID); ok {
		t.Fatalf("failed create entered service memory: %#v", got)
	}
}
