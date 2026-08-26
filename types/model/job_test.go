package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestJobInitialInteractiveConfigurationJSONRoundTrip(t *testing.T) {
	want := &Job{
		ID:                     "job-config",
		InitialAgentID:         "claude",
		FirstModelID:           "claude-sonnet",
		InitialACPMode:         "plan",
		InitialACPThoughtLevel: "high",
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal Job: %v", err)
	}
	var got Job
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal Job: %v", err)
	}
	if got.InitialAgentID != want.InitialAgentID ||
		got.FirstModelID != want.FirstModelID ||
		got.InitialACPMode != want.InitialACPMode ||
		got.InitialACPThoughtLevel != want.InitialACPThoughtLevel {
		t.Fatalf("interactive configuration changed during JSON round trip: got %+v want %+v", got, *want)
	}

	var legacy Job
	if err := json.Unmarshal([]byte(`{"id":"legacy","sessionIds":[]}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy Job: %v", err)
	}
	if legacy.InitialAgentID != "" || legacy.InitialACPMode != "" || legacy.InitialACPThoughtLevel != "" {
		t.Fatalf("legacy Job should keep zero-value initial configuration: %+v", legacy)
	}
}

func TestJobDeepCopyIsIndependent(t *testing.T) {
	assertDeepCopyFieldsCovered(t, reflect.TypeOf(Job{}), []string{"SessionIDs", "GraphSessionIDs", "Progress", "ClientMessageReceipts", "CommandReceipts", "MessageQueue"})
	assertDeepCopyFieldsCovered(t, reflect.TypeOf(JobProgress{}), []string{"PersistWarnings"})

	orig := &Job{
		ID:              "job-1",
		SessionIDs:      []string{"session-1", "session-2"},
		GraphSessionIDs: []string{"graph-session-1", "graph-session-2"},
		Progress: &JobProgress{
			LastError:       "original error",
			PersistWarnings: []string{"warning-1"},
		},
		ClientMessageReceipts: map[string]ClientMessageReceipt{
			"client-1": {State: ClientMessageStateCompleted, PayloadHash: "hash-1"},
		},
		CommandReceipts: map[string]CommandReceipt{
			"command-1": {
				PayloadHash: "command-hash-1",
				Event: &CommandSystemMessageEvent{
					Command: "/new",
					Action:  &CommandAction{Type: "new_job", WorkspaceID: "ws-1"},
				},
			},
		},
		MessageQueue: []QueuedJobMessage{{
			ID:       "queued-1",
			Messages: []RequestMessage{{Content: "queued original", ImageUrls: []string{"image-original"}}},
		}},
		CreationClientMessageID: "create-client-1",
		CreationPayloadHash:     "create-hash-1",
	}

	cp := orig.DeepCopy()

	cp.SessionIDs[0] = "copy-session"
	cp.GraphSessionIDs[0] = "copy-graph-session"
	cp.Progress.LastError = "copy error"
	cp.Progress.PersistWarnings[0] = "copy warning"
	cp.ClientMessageReceipts["client-1"] = ClientMessageReceipt{State: ClientMessageStateFailed}
	cp.ClientMessageReceipts["copy-only"] = ClientMessageReceipt{State: ClientMessageStateProcessing}
	commandReceipt := cp.CommandReceipts["command-1"]
	commandReceipt.Event.Text = "copy result"
	commandReceipt.Event.Action.WorkspaceID = "copy-ws"
	cp.CommandReceipts["command-1"] = commandReceipt
	cp.CommandReceipts["copy-only"] = CommandReceipt{PayloadHash: "copy"}
	cp.MessageQueue[0].Messages[0].Content = "queued copy"
	cp.MessageQueue[0].Messages[0].ImageUrls[0] = "image-copy"

	if orig.SessionIDs[0] != "session-1" {
		t.Fatalf("orig SessionIDs mutated via copy: got %q", orig.SessionIDs[0])
	}
	if orig.GraphSessionIDs[0] != "graph-session-1" {
		t.Fatalf("orig GraphSessionIDs mutated via copy: got %q", orig.GraphSessionIDs[0])
	}
	if got := orig.Progress.LastError; got != "original error" {
		t.Fatalf("orig LastError mutated via copy: got %q", got)
	}
	if got := orig.Progress.PersistWarnings[0]; got != "warning-1" {
		t.Fatalf("orig PersistWarnings mutated via copy: got %q", got)
	}
	if got := orig.ClientMessageReceipts["client-1"].State; got != ClientMessageStateCompleted {
		t.Fatalf("orig ClientMessageReceipts mutated via copy: got %q", got)
	}
	if _, ok := orig.ClientMessageReceipts["copy-only"]; ok {
		t.Fatal("orig ClientMessageReceipts gained key added on copy")
	}
	if got := orig.CommandReceipts["command-1"].Event.Text; got != "" {
		t.Fatalf("orig CommandReceipts event mutated via copy: got %q", got)
	}
	if got := orig.CommandReceipts["command-1"].Event.Action.WorkspaceID; got != "ws-1" {
		t.Fatalf("orig CommandReceipts action mutated via copy: got %q", got)
	}
	if _, ok := orig.CommandReceipts["copy-only"]; ok {
		t.Fatal("orig CommandReceipts gained key added on copy")
	}
	if got := orig.MessageQueue[0].Messages[0].Content; got != "queued original" {
		t.Fatalf("orig MessageQueue content mutated via copy: got %q", got)
	}
	if got := orig.MessageQueue[0].Messages[0].ImageUrls[0]; got != "image-original" {
		t.Fatalf("orig MessageQueue image mutated via copy: got %q", got)
	}
	if cp.CreationClientMessageID != orig.CreationClientMessageID || cp.CreationPayloadHash != orig.CreationPayloadHash {
		t.Fatalf("creation idempotency fields changed in copy: got (%q,%q), want (%q,%q)", cp.CreationClientMessageID, cp.CreationPayloadHash, orig.CreationClientMessageID, orig.CreationPayloadHash)
	}

	orig.SessionIDs[1] = "orig-session"
	orig.GraphSessionIDs[1] = "orig-graph-session"
	orig.Progress.LastError = "orig changed"
	orig.MessageQueue[0].Messages[0].Content = "queued orig changed"
	orig.MessageQueue[0].Messages[0].ImageUrls[0] = "image-orig-changed"

	if cp.SessionIDs[1] != "session-2" {
		t.Fatalf("copy SessionIDs mutated via orig: got %q", cp.SessionIDs[1])
	}
	if cp.GraphSessionIDs[1] != "graph-session-2" {
		t.Fatalf("copy GraphSessionIDs mutated via orig: got %q", cp.GraphSessionIDs[1])
	}
	if got := cp.Progress.LastError; got != "copy error" {
		t.Fatalf("copy LastError mutated via orig: got %q", got)
	}
	if got := cp.MessageQueue[0].Messages[0].Content; got != "queued copy" {
		t.Fatalf("copy MessageQueue content mutated via orig: got %q", got)
	}
	if got := cp.MessageQueue[0].Messages[0].ImageUrls[0]; got != "image-copy" {
		t.Fatalf("copy MessageQueue image mutated via orig: got %q", got)
	}
}

func assertDeepCopyFieldsCovered(t *testing.T, typ reflect.Type, covered []string) {
	t.Helper()
	coveredSet := make(map[string]struct{}, len(covered))
	for _, name := range covered {
		coveredSet[name] = struct{}{}
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		switch field.Type.Kind() {
		case reflect.Map, reflect.Ptr, reflect.Slice:
			if _, ok := coveredSet[field.Name]; !ok {
				t.Fatalf("%s.%s is %s but is not covered by TestJobDeepCopyIsIndependent", typ.Name(), field.Name, field.Type.Kind())
			}
		}
	}
}
