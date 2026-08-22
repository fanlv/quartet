package job

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

func idempotencyCommandEvent(jobID, clientMessageID string) *model.CommandSystemMessageEvent {
	return &model.CommandSystemMessageEvent{
		BaseEvent: model.BaseEvent{
			Type:      model.EventTypeCommandSystemMessage,
			SessionID: "session-idempotency",
			JobID:     jobID,
			Timestamp: 1234,
		},
		ClientMessageID: clientMessageID,
		Command:         "/ws",
		Text:            "switched workspace",
		Present:         "inline",
		Action: &model.CommandAction{
			Type:        "switch_workspace",
			WorkspaceID: "ws-2",
		},
	}
}

func assertIdempotencyCommandEvent(t *testing.T, got, want *model.CommandSystemMessageEvent) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command event = %#v, want %#v", got, want)
	}
}

func TestExecuteCommandConcurrentDuplicateExecutesOnce(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	const (
		jobID           = "job-command-concurrent"
		clientMessageID = "command-concurrent"
		payload         = "/ws ws-2"
	)
	addIdempotencyJob(svc, jobID)
	wantEvent := idempotencyCommandEvent(jobID, clientMessageID)

	var callbackCalls atomic.Int32
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	var startedOnce sync.Once
	execute := func() *model.CommandSystemMessageEvent {
		callbackCalls.Add(1)
		startedOnce.Do(func() { close(callbackStarted) })
		<-releaseCallback
		return wantEvent
	}

	type outcome struct {
		event     *model.CommandSystemMessageEvent
		duplicate bool
		err       error
	}
	outcomes := make(chan outcome, 2)
	call := func() {
		event, duplicate, err := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, payload, execute)
		outcomes <- outcome{event: event, duplicate: duplicate, err: err}
	}

	go call()
	select {
	case <-callbackStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first command callback")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		call()
	}()
	<-secondStarted
	close(releaseCallback)

	duplicateCounts := map[bool]int{}
	for i := 0; i < 2; i++ {
		select {
		case got := <-outcomes:
			if got.err != nil {
				t.Fatalf("concurrent ExecuteCommand returned error: %v", got.err)
			}
			assertIdempotencyCommandEvent(t, got.event, wantEvent)
			duplicateCounts[got.duplicate]++
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent ExecuteCommand")
		}
	}
	if duplicateCounts[false] != 1 || duplicateCounts[true] != 1 {
		t.Fatalf("duplicate results = %#v, want one original and one duplicate", duplicateCounts)
	}
	if got := callbackCalls.Load(); got != 1 {
		t.Fatalf("command callback calls = %d, want 1", got)
	}
	if got := repo.saves(); got != 1 {
		t.Fatalf("Save calls = %d, want 1", got)
	}
	persisted, ok := repo.saved(jobID)
	if !ok {
		t.Fatal("command receipt was not persisted")
	}
	receipt, ok := persisted.CommandReceipts[clientMessageID]
	if !ok {
		t.Fatalf("persisted command receipt %q not found", clientMessageID)
	}
	assertIdempotencyCommandEvent(t, receipt.Event, wantEvent)
}

func TestExecuteCommandDuplicateAfterCompletionReplaysEvent(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	const (
		jobID           = "job-command-completed"
		clientMessageID = "command-completed"
		payload         = "/ws ws-2"
	)
	addIdempotencyJob(svc, jobID)
	wantEvent := idempotencyCommandEvent(jobID, clientMessageID)

	var callbackCalls atomic.Int32
	first, duplicate, err := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, payload, func() *model.CommandSystemMessageEvent {
		callbackCalls.Add(1)
		return wantEvent
	})
	if err != nil {
		t.Fatalf("first ExecuteCommand: %v", err)
	}
	if duplicate {
		t.Fatal("first ExecuteCommand reported duplicate")
	}
	assertIdempotencyCommandEvent(t, first, wantEvent)

	replayed, duplicate, err := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, payload, func() *model.CommandSystemMessageEvent {
		callbackCalls.Add(1)
		return &model.CommandSystemMessageEvent{Text: "must not be returned"}
	})
	if err != nil {
		t.Fatalf("duplicate ExecuteCommand: %v", err)
	}
	if !duplicate {
		t.Fatal("completed ExecuteCommand retry did not report duplicate")
	}
	assertIdempotencyCommandEvent(t, replayed, first)
	if got := callbackCalls.Load(); got != 1 {
		t.Fatalf("command callback calls = %d, want 1", got)
	}
	if got := repo.saves(); got != 1 {
		t.Fatalf("Save calls = %d, want 1", got)
	}
}

func TestExecuteCommandRejectsDifferentPayloadForClientMessageID(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	const (
		jobID           = "job-command-conflict"
		clientMessageID = "command-conflict"
	)
	addIdempotencyJob(svc, jobID)
	wantEvent := idempotencyCommandEvent(jobID, clientMessageID)

	if _, duplicate, err := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, "/ws ws-2", func() *model.CommandSystemMessageEvent {
		return wantEvent
	}); err != nil || duplicate {
		t.Fatalf("first ExecuteCommand = (duplicate=%t, err=%v), want success", duplicate, err)
	}

	var conflictingCalls atomic.Int32
	event, duplicate, err := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, "/ws ws-3", func() *model.CommandSystemMessageEvent {
		conflictingCalls.Add(1)
		return &model.CommandSystemMessageEvent{Text: "must not execute"}
	})
	if !errors.Is(err, ErrClientMessageIDConflict) {
		t.Fatalf("conflicting ExecuteCommand error = %v, want ErrClientMessageIDConflict", err)
	}
	if event != nil || duplicate {
		t.Fatalf("conflicting ExecuteCommand = (%#v, %t), want nil event and non-duplicate", event, duplicate)
	}
	if got := conflictingCalls.Load(); got != 0 {
		t.Fatalf("conflicting command callback calls = %d, want 0", got)
	}
	if got := repo.saves(); got != 1 {
		t.Fatalf("Save calls = %d, want 1", got)
	}
}

func TestExecuteCommandPersistFailureDoesNotExposeReceiptAndAllowsRetry(t *testing.T) {
	repo := newIdempotencyTestRepo()
	svc := newIdempotencyService(repo)
	const (
		jobID           = "job-command-save-failure"
		clientMessageID = "command-save-failure"
		payload         = "/ws ws-2"
	)
	job := addIdempotencyJob(svc, jobID)
	repo.seed(job)
	wantEvent := idempotencyCommandEvent(jobID, clientMessageID)

	var callbackCalls atomic.Int32
	execute := func() *model.CommandSystemMessageEvent {
		callbackCalls.Add(1)
		return wantEvent
	}
	saveErr := errors.New("forced command receipt save failure")
	repo.failNext(1, saveErr)

	event, duplicate, err := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, payload, execute)
	if !errors.Is(err, saveErr) {
		t.Fatalf("first ExecuteCommand error = %v, want forced save error", err)
	}
	if event != nil || duplicate {
		t.Fatalf("failed ExecuteCommand = (%#v, %t), want nil event and non-duplicate", event, duplicate)
	}
	if got := callbackCalls.Load(); got != 1 {
		t.Fatalf("command callback calls after failed save = %d, want 1", got)
	}
	inMemory, ok := svc.Get(jobID)
	if !ok {
		t.Fatal("job disappeared after command receipt save failure")
	}
	if receipt, exists := inMemory.CommandReceipts[clientMessageID]; exists {
		t.Fatalf("failed command receipt exposed in memory: %#v", receipt)
	}
	persisted, ok := repo.saved(jobID)
	if !ok {
		t.Fatal("seeded job disappeared after command receipt save failure")
	}
	if receipt, exists := persisted.CommandReceipts[clientMessageID]; exists {
		t.Fatalf("failed command receipt reached durable storage: %#v", receipt)
	}

	// A leaked in-memory receipt would turn this into a successful duplicate
	// even though no callback is supplied. It must instead behave as unclaimed.
	leakedEvent, leakedDuplicate, leakedErr := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, payload, nil)
	if leakedErr == nil || leakedEvent != nil || leakedDuplicate {
		t.Fatalf("receipt probe after failed save = (%#v, %t, %v), want callback-required error", leakedEvent, leakedDuplicate, leakedErr)
	}

	retried, duplicate, err := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, payload, execute)
	if err != nil {
		t.Fatalf("retry ExecuteCommand: %v", err)
	}
	if duplicate {
		t.Fatal("retry after failed persistence reported duplicate")
	}
	assertIdempotencyCommandEvent(t, retried, wantEvent)
	if got := callbackCalls.Load(); got != 2 {
		t.Fatalf("command callback calls after successful retry = %d, want 2", got)
	}
	if got := repo.saves(); got != 2 {
		t.Fatalf("Save calls = %d, want failed attempt plus successful retry", got)
	}
	persisted, ok = repo.saved(jobID)
	if !ok {
		t.Fatal("job missing after successful command retry")
	}
	receipt, ok := persisted.CommandReceipts[clientMessageID]
	if !ok {
		t.Fatal("successful command retry did not persist receipt")
	}
	assertIdempotencyCommandEvent(t, receipt.Event, wantEvent)
}

func TestClientMessageIDCannotChangeBetweenCommandAndAgentMessage(t *testing.T) {
	tests := []struct {
		name         string
		commandFirst bool
	}{
		{name: "command then Agent message", commandFirst: true},
		{name: "Agent message then command", commandFirst: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newIdempotencyTestRepo()
			svc := newIdempotencyService(repo)
			const clientMessageID = "shared-client-id"
			jobID := "job-cross-type-" + tt.name
			addIdempotencyJob(svc, jobID)
			opts := idempotencyTestOptions(clientMessageID, "/help")
			runner := newIdempotencyTestRunner(nil, nil)
			commandCalls := 0
			execute := func() *model.CommandSystemMessageEvent {
				commandCalls++
				return idempotencyCommandEvent(jobID, clientMessageID)
			}

			if tt.commandFirst {
				if _, _, err := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, "/help", execute); err != nil {
					t.Fatalf("first ExecuteCommand: %v", err)
				}
				if _, err := svc.SendMessage(context.Background(), jobID, runner, opts); !errors.Is(err, ErrClientMessageIDConflict) {
					t.Fatalf("SendMessage after command error=%v, want conflict", err)
				}
			} else {
				if _, err := svc.SendMessage(context.Background(), jobID, runner, opts); err != nil {
					t.Fatalf("first SendMessage: %v", err)
				}
				waitForIdempotencyReceipt(t, svc, jobID, opts, model.ClientMessageStateCompleted)
				if _, _, err := svc.ExecuteCommand(context.Background(), jobID, clientMessageID, "/help", execute); !errors.Is(err, ErrClientMessageIDConflict) {
					t.Fatalf("ExecuteCommand after Agent message error=%v, want conflict", err)
				}
			}
			if tt.commandFirst && runner.callCount() != 0 {
				t.Fatalf("Agent executed after cross-type conflict: %d", runner.callCount())
			}
			if !tt.commandFirst && commandCalls != 0 {
				t.Fatalf("command callback executed after cross-type conflict: %d", commandCalls)
			}
		})
	}
}
