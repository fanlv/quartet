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
	assertDeepCopyFieldsCovered(t, reflect.TypeOf(Job{}), []string{"LoopConfig", "SessionIDs", "GraphSessionIDs", "Progress", "Resume", "ClientMessageReceipts", "CommandReceipts"})
	assertDeepCopyFieldsCovered(t, reflect.TypeOf(LoopConfig{}), []string{"Flow", "Variables", "DisabledVars", "Rounds"})
	assertDeepCopyFieldsCovered(t, reflect.TypeOf(FlowNode{}), []string{"Children"})
	assertDeepCopyFieldsCovered(t, reflect.TypeOf(JobProgress{}), []string{"CurrentPath", "Results", "PersistWarnings", "GroupActualIterations", "GroupActualLeafCounts", "SkippedPaths"})
	assertDeepCopyFieldsCovered(t, reflect.TypeOf(JobResume{}), []string{"NextPath"})
	assertDeepCopyFieldsCovered(t, reflect.TypeOf(IterationResult{}), []string{"Path"})

	orig := &Job{
		ID:              "job-1",
		SessionIDs:      []string{"session-1", "session-2"},
		GraphSessionIDs: []string{"graph-session-1", "graph-session-2"},
		LoopConfig: &LoopConfig{
			Flow: []FlowNode{
				{
					ID:             "group-1",
					Type:           FlowNodeTypeGroup,
					IterationCount: 2,
					Children: []FlowNode{
						{ID: "step-1", Type: FlowNodeTypeStep, Message: "original message"},
					},
				},
			},
			Variables:    map[string]string{"name": "original"},
			DisabledVars: []string{"name"},
			Rounds: []LoopRound{
				{Message: "legacy original", RepeatCount: 1, RoundMode: RoundModeNone},
			},
		},
		Progress: &JobProgress{
			TotalSteps:            10,
			CurrentPath:           []int{0, 0},
			Results:               []IterationResult{{Path: []int{0, 0}, SessionID: "session-1", Success: true}},
			PersistWarnings:       []string{"warning-1"},
			GroupActualIterations: map[string]int{"0": 2},
			GroupActualLeafCounts: map[string]int{"0": 3},
			SkippedPaths:          map[string]bool{"0.0.1.0": true},
		},
		Resume: &JobResume{NextPath: []int{1, 0}, SessionID: "session-2"},
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
		CreationClientMessageID: "create-client-1",
		CreationPayloadHash:     "create-hash-1",
	}

	cp := orig.DeepCopy()

	cp.SessionIDs[0] = "copy-session"
	cp.GraphSessionIDs[0] = "copy-graph-session"
	cp.LoopConfig.Flow[0].Children[0].Message = "copy message"
	cp.LoopConfig.Variables["name"] = "copy"
	cp.LoopConfig.Variables["copy-only"] = "present"
	cp.LoopConfig.DisabledVars[0] = "copy-disabled"
	cp.LoopConfig.Rounds[0].Message = "legacy copy"
	cp.Progress.CurrentPath[0] = 9
	cp.Progress.Results[0].Path[0] = 9
	cp.Progress.PersistWarnings[0] = "copy warning"
	cp.Progress.GroupActualIterations["0"] = 99
	cp.Progress.GroupActualIterations["copy-only"] = 5
	cp.Progress.GroupActualLeafCounts["0"] = 88
	cp.Progress.GroupActualLeafCounts["copy-only"] = 6
	cp.Progress.SkippedPaths["copy-only"] = true
	cp.Resume.NextPath[0] = 9
	cp.ClientMessageReceipts["client-1"] = ClientMessageReceipt{State: ClientMessageStateFailed}
	cp.ClientMessageReceipts["copy-only"] = ClientMessageReceipt{State: ClientMessageStateProcessing}
	commandReceipt := cp.CommandReceipts["command-1"]
	commandReceipt.Event.Text = "copy result"
	commandReceipt.Event.Action.WorkspaceID = "copy-ws"
	cp.CommandReceipts["command-1"] = commandReceipt
	cp.CommandReceipts["copy-only"] = CommandReceipt{PayloadHash: "copy"}

	if orig.SessionIDs[0] != "session-1" {
		t.Fatalf("orig SessionIDs mutated via copy: got %q", orig.SessionIDs[0])
	}
	if orig.GraphSessionIDs[0] != "graph-session-1" {
		t.Fatalf("orig GraphSessionIDs mutated via copy: got %q", orig.GraphSessionIDs[0])
	}
	if got := orig.LoopConfig.Flow[0].Children[0].Message; got != "original message" {
		t.Fatalf("orig Flow child mutated via copy: got %q", got)
	}
	if got := orig.LoopConfig.Variables["name"]; got != "original" {
		t.Fatalf("orig Variables mutated via copy: got %q", got)
	}
	if _, ok := orig.LoopConfig.Variables["copy-only"]; ok {
		t.Fatalf("orig Variables gained key added on copy")
	}
	if got := orig.LoopConfig.DisabledVars[0]; got != "name" {
		t.Fatalf("orig DisabledVars mutated via copy: got %q", got)
	}
	if got := orig.LoopConfig.Rounds[0].Message; got != "legacy original" {
		t.Fatalf("orig Rounds mutated via copy: got %q", got)
	}
	if got := orig.Progress.CurrentPath[0]; got != 0 {
		t.Fatalf("orig CurrentPath mutated via copy: got %d", got)
	}
	if got := orig.Progress.Results[0].Path[0]; got != 0 {
		t.Fatalf("orig Results[0].Path mutated via copy: got %d", got)
	}
	if got := orig.Progress.PersistWarnings[0]; got != "warning-1" {
		t.Fatalf("orig PersistWarnings mutated via copy: got %q", got)
	}
	if got := orig.Progress.GroupActualIterations["0"]; got != 2 {
		t.Fatalf("orig GroupActualIterations mutated via copy: got %d", got)
	}
	if _, ok := orig.Progress.GroupActualIterations["copy-only"]; ok {
		t.Fatalf("orig GroupActualIterations gained key added on copy")
	}
	if got := orig.Progress.GroupActualLeafCounts["0"]; got != 3 {
		t.Fatalf("orig GroupActualLeafCounts mutated via copy: got %d", got)
	}
	if _, ok := orig.Progress.GroupActualLeafCounts["copy-only"]; ok {
		t.Fatalf("orig GroupActualLeafCounts gained key added on copy")
	}
	if _, ok := orig.Progress.SkippedPaths["copy-only"]; ok {
		t.Fatalf("orig SkippedPaths gained key added on copy")
	}
	if got := orig.Resume.NextPath[0]; got != 1 {
		t.Fatalf("orig Resume.NextPath mutated via copy: got %d", got)
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
	if cp.CreationClientMessageID != orig.CreationClientMessageID || cp.CreationPayloadHash != orig.CreationPayloadHash {
		t.Fatalf("creation idempotency fields changed in copy: got (%q,%q), want (%q,%q)", cp.CreationClientMessageID, cp.CreationPayloadHash, orig.CreationClientMessageID, orig.CreationPayloadHash)
	}

	orig.SessionIDs[1] = "orig-session"
	orig.GraphSessionIDs[1] = "orig-graph-session"
	orig.LoopConfig.Flow[0].Children[0].ID = "orig-step"
	orig.LoopConfig.Variables["name"] = "orig-again"
	orig.LoopConfig.Rounds[0].RepeatCount = 7
	orig.Progress.Results[0].Path[1] = 7
	orig.Resume.NextPath[1] = 7

	if cp.SessionIDs[1] != "session-2" {
		t.Fatalf("copy SessionIDs mutated via orig: got %q", cp.SessionIDs[1])
	}
	if cp.GraphSessionIDs[1] != "graph-session-2" {
		t.Fatalf("copy GraphSessionIDs mutated via orig: got %q", cp.GraphSessionIDs[1])
	}
	if got := cp.LoopConfig.Flow[0].Children[0].ID; got != "step-1" {
		t.Fatalf("copy Flow child mutated via orig: got %q", got)
	}
	if got := cp.LoopConfig.Variables["name"]; got != "copy" {
		t.Fatalf("copy Variables mutated via orig: got %q", got)
	}
	if got := cp.LoopConfig.Rounds[0].RepeatCount; got != 1 {
		t.Fatalf("copy Rounds mutated via orig: got %d", got)
	}
	if got := cp.Progress.Results[0].Path[1]; got != 0 {
		t.Fatalf("copy Results[0].Path mutated via orig: got %d", got)
	}
	if got := cp.Resume.NextPath[1]; got != 0 {
		t.Fatalf("copy Resume.NextPath mutated via orig: got %d", got)
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

// DeepCopy must independently copy the GroupActualIterations / GroupActualLeafCounts
// maps so the live job and a saved snapshot don't share them — otherwise a
// concurrent backfill mutation races json.Marshal of the snapshot. Regression test.
func TestJobDeepCopyIsolatesGroupActualIterations(t *testing.T) {
	orig := &Job{
		ID: "job-1",
		Progress: &JobProgress{
			TotalSteps:            10,
			GroupActualIterations: map[string]int{"0": 2},
			GroupActualLeafCounts: map[string]int{"0": 3},
		},
	}

	cp := orig.DeepCopy()

	if cp.Progress.GroupActualIterations == nil {
		t.Fatalf("copy GroupActualIterations is nil, want copied map")
	}
	if cp.Progress.GroupActualLeafCounts == nil {
		t.Fatalf("copy GroupActualLeafCounts is nil, want copied map")
	}
	// Mutating the copy must not touch the original, and vice versa.
	cp.Progress.GroupActualIterations["0"] = 99
	cp.Progress.GroupActualIterations["1"] = 5
	cp.Progress.GroupActualLeafCounts["0"] = 88
	cp.Progress.GroupActualLeafCounts["1"] = 6
	if orig.Progress.GroupActualIterations["0"] != 2 {
		t.Fatalf("orig map mutated via copy: got %d, want 2", orig.Progress.GroupActualIterations["0"])
	}
	if orig.Progress.GroupActualLeafCounts["0"] != 3 {
		t.Fatalf("orig leaf map mutated via copy: got %d, want 3", orig.Progress.GroupActualLeafCounts["0"])
	}
	if _, ok := orig.Progress.GroupActualIterations["1"]; ok {
		t.Fatalf("orig map gained key '1' added on copy — shared map")
	}
	if _, ok := orig.Progress.GroupActualLeafCounts["1"]; ok {
		t.Fatalf("orig leaf map gained key '1' added on copy — shared map")
	}

	orig.Progress.GroupActualIterations["0"] = 7
	orig.Progress.GroupActualLeafCounts["0"] = 9
	if cp.Progress.GroupActualIterations["0"] != 99 {
		t.Fatalf("copy map mutated via orig: got %d, want 99", cp.Progress.GroupActualIterations["0"])
	}
	if cp.Progress.GroupActualLeafCounts["0"] != 88 {
		t.Fatalf("copy leaf map mutated via orig: got %d, want 88", cp.Progress.GroupActualLeafCounts["0"])
	}
}
