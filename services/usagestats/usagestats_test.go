package usagestats

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestResolveToolBucketKey(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     string
		want     string
	}{
		{"non-shell verbatim", "file_read", `{"path":"/etc/passwd"}`, "FILE_READ"},
		{"non-shell with path argument", "Read", `{"path":"/etc/passwd"}`, "READ"},
		{"non-shell title carries path", "Read /etc/passwd", `{}`, "READ"},
		{"non-shell title carries multi-word", "Read File", `{}`, "READ"},
		{"non-shell grep with path", "Grep /data00/home/fanlv/quartet", `{}`, "GREP"},
		{"shell tool, json command", "shell", `{"command":"ls -la /tmp"}`, "LS"},
		{"bash tool, json command", "bash_exec", `{"command":"git status"}`, "GIT"},
		{"shell with env prefix", "shell", `{"command":"FOO=bar ls"}`, "LS"},
		{"shell with multi env", "shell", `{"command":"A=1 B=2 python3 main.py"}`, "PYTHON3"},
		{"shell with quoted env value", "shell", `{"command":"FOO=\"a b\" git status"}`, "GIT"},
		{"shell quoted command", "shell", `{"command":"\"my cmd\" arg"}`, "MY CMD"},
		{"shell raw args (no JSON) falls back", "shell", `cat foo`, "SHELL"},
		{"shell malformed json falls back", "shell", `{"command":"git status"`, "SHELL"},
		{"shell empty command falls back", "shell", `{"command":""}`, "SHELL"},
		{"non-shell name, ignore command field", "grep_tool", `{"command":"foo"}`, "GREP_TOOL"},
		{"empty tool name maps to unnamed", "", `{}`, unnamedToolKey},
		{"shell pipe takes first command", "bash", `{"command":"cat foo | grep bar"}`, "CAT"},
		{"shell sudo wrapper not unwrapped", "shell", `{"command":"sudo ls"}`, "SUDO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveToolBucketKey(tt.toolName, tt.args)
			if got != tt.want {
				t.Errorf("ResolveToolBucketKey(%q, %q) = %q, want %q", tt.toolName, tt.args, got, tt.want)
			}
		})
	}
}

func TestGetUsageSurfacesParseError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.Local)
	dir := filepath.Join(root, "quartet", "usage-stats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-05.json"), []byte(`{broken`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	svc, err := NewService(nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, err := svc.GetUsage(now, now); err == nil {
		t.Fatal("GetUsage() error = nil, want parse error")
	}
	// Cached failed reads must continue surfacing the original problem instead
	// of looking like a genuine empty month on the next request.
	if _, err := svc.GetUsage(now, now); err == nil {
		t.Fatal("second GetUsage() error = nil, want cached parse error")
	}
}

// TestRecorderRoundTrip exercises the write → read path: record three
// shell-style snapshots and verify the aggregates line up.
func TestRecorderRoundTrip(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	svc, err := NewService(nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	now := time.Now()
	for i := 0; i < 3; i++ {
		acc := NewAccumulator()
		acc.OnAssistantText(nil, "hello world")
		callID := "call-" + strconv.Itoa(i)
		acc.OnToolCallStart(callID, "shell", now.UnixMilli())
		acc.OnToolCallArgsDelta(callID, `{"command":"ls -la"}`)
		acc.OnToolCallEnd(nil, callID, now.UnixMilli()+500)
		acc.OnTokenUsage(42)
		snap := acc.SnapshotWithEventID("evt-"+strconv.Itoa(i), "ws-test", "claude", now.UnixMilli(), 200)
		svc.Record(snap)
	}
	svc.Flush(nil)

	report, err := svc.GetUsage(now, now)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}

	if got := totalTurnCount(report.ByWorkspace, "ws-test"); got != 3 {
		t.Errorf("workspace turn count = %d, want 3", got)
	}
	if got := totalTurnCount(report.ByWorkspace, "ws-test"); got != 3 {
		t.Errorf("workspace turns = %d, want 3", got)
	}
	if got := totalToolCount(report.ByTool, "LS"); got != 3 {
		t.Errorf("LS tool calls = %d, want 3", got)
	}
	// Three shell tool calls = three increments to toolCallCount.
	for _, r := range report.ByModel {
		if r.ModelID == "claude" {
			if r.ToolCallCount != 3 {
				t.Errorf("claude toolCallCount = %d, want 3", r.ToolCallCount)
			}
		}
	}
}

// TestAccumulatorTokenUsageIsLastValue ensures total tokens
// reflect the last OnTokenUsage value per step (not summed within the step).
func TestAccumulatorTokenUsageIsLastValue(t *testing.T) {
	acc := NewAccumulator()
	acc.OnTokenUsage(10)
	acc.OnTokenUsage(20)
	snap := acc.SnapshotWithEventID("evt-last-wins", "ws", "m", time.Now().UnixMilli(), 100)
	if snap.Tokens.Total != 20 || snap.Tokens.Estimated != 20 {
		t.Errorf("total tokens = %d estimated = %d, want 20 (last wins)", snap.Tokens.Total, snap.Tokens.Estimated)
	}
}

// TestAccumulatorPendingToolDropsOnSnapshot verifies that an open tool call
// (no matching End) is silently discarded — no count, no duration.
func TestAccumulatorPendingToolDropsOnSnapshot(t *testing.T) {
	acc := NewAccumulator()
	acc.OnToolCallStart("never-ends", "shell", time.Now().UnixMilli())
	acc.OnToolCallArgsDelta("never-ends", `{"command":"sleep 99"}`)
	snap := acc.SnapshotWithEventID("evt-pending-tool", "ws", "m", time.Now().UnixMilli(), 100)
	if snap.ToolCallCount != 0 {
		t.Errorf("toolCallCount = %d, want 0 for un-ended call", snap.ToolCallCount)
	}
	if len(snap.Tools) != 0 {
		t.Errorf("tools map size = %d, want 0", len(snap.Tools))
	}
}

func TestModelAggregatesSurfaceUnattributedResidual(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	svc, err := NewService(nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	now := time.Now()

	svc.Record(Snapshot{
		EventID:        "evt-attributed",
		WorkspaceID:    "ws-test",
		ModelID:        "claude",
		FinishedAtMs:   now.UnixMilli(),
		DurationMs:     100,
		AssistantCount: 1,
		Tokens:         TokenTotals{Total: 10, Assistant: 5},
	})
	svc.Record(Snapshot{
		EventID:        "evt-unattributed",
		WorkspaceID:    "ws-test",
		FinishedAtMs:   now.UnixMilli(),
		DurationMs:     50,
		AssistantCount: 1,
		Tokens:         TokenTotals{Total: 6, Assistant: 3},
	})

	report, err := svc.GetUsage(now, now)
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if got := totalModelDuration(report.ByModel, "claude"); got != 100 {
		t.Fatalf("claude duration = %d, want 100", got)
	}
	if got := totalModelDuration(report.ByModel, ""); got != 50 {
		t.Fatalf("unattributed model duration = %d, want 50", got)
	}
	if len(report.Daily) != 1 {
		t.Fatalf("daily rows = %d, want 1", len(report.Daily))
	}
	if got := report.Daily[0].Models[""].TotalMs; got != 50 {
		t.Fatalf("daily unattributed model duration = %d, want 50", got)
	}
	if got := report.Daily[0].TotalMs; got != 150 {
		t.Fatalf("daily total duration = %d, want 150", got)
	}
}

func totalTurnCount(rows []WorkspaceAggregate, ws string) int {
	for _, r := range rows {
		if r.WorkspaceID == ws {
			return r.TurnCount
		}
	}
	return 0
}

func totalToolCount(rows []ToolAggregate, key string) int {
	for _, r := range rows {
		if r.ToolKey == key {
			return r.Count
		}
	}
	return 0
}

func totalModelDuration(rows []ModelAggregate, modelID string) int64 {
	for _, r := range rows {
		if r.ModelID == modelID {
			return r.TotalMs
		}
	}
	return 0
}
