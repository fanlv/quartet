package graph

import "testing"

func TestMergeVisibleSnapshots_Union(t *testing.T) {
	ups := []UpstreamSnapshot{
		{NodeID: "n1", Variables: map[string]string{"a": "1"}, LastAssistantMsg: "msg1"},
		{NodeID: "n2", Variables: map[string]string{"b": "2"}, LastAssistantMsg: "msg2"},
	}
	merged := MergeVisibleSnapshots(ups)
	if merged["a"] != "1" || merged["b"] != "2" {
		t.Fatalf("union wrong: %v", merged)
	}
}

func TestMergeVisibleSnapshots_LastAssistantByAscendingNodeID(t *testing.T) {
	// _last_assistant_msg = greatest node ID's value, independent of input order.
	ups := []UpstreamSnapshot{
		{NodeID: "n2", LastAssistantMsg: "from-n2"},
		{NodeID: "n1", LastAssistantMsg: "from-n1"},
		{NodeID: "n3", LastAssistantMsg: "from-n3"},
	}
	merged := MergeVisibleSnapshots(ups)
	if merged[reservedLastAssistant] != "from-n3" {
		t.Fatalf("_last_assistant_msg = %q, want from-n3", merged[reservedLastAssistant])
	}

	// Reversed input order must give the same deterministic result.
	rev := []UpstreamSnapshot{ups[2], ups[0], ups[1]}
	if MergeVisibleSnapshots(rev)[reservedLastAssistant] != "from-n3" {
		t.Fatal("result must not depend on input order")
	}
}

func TestMergeVisibleSnapshots_SnapshotCopyOfLastAssistantIgnored(t *testing.T) {
	// A snapshot carrying its own _last_assistant_msg must not win the union by
	// arrival; the dedicated LastAssistantMsg field of the greatest node ID wins.
	ups := []UpstreamSnapshot{
		{NodeID: "n1", Variables: map[string]string{reservedLastAssistant: "stale"}, LastAssistantMsg: "fresh1"},
		{NodeID: "n2", LastAssistantMsg: "fresh2"},
	}
	merged := MergeVisibleSnapshots(ups)
	if merged[reservedLastAssistant] != "fresh2" {
		t.Fatalf("_last_assistant_msg = %q, want fresh2", merged[reservedLastAssistant])
	}
}

func TestMergeVisibleSnapshots_SingleUpstreamInherits(t *testing.T) {
	ups := []UpstreamSnapshot{
		{NodeID: "n1", Variables: map[string]string{"a": "1", "b": "2"}, LastAssistantMsg: "only"},
	}
	merged := MergeVisibleSnapshots(ups)
	if merged["a"] != "1" || merged["b"] != "2" || merged[reservedLastAssistant] != "only" {
		t.Fatalf("single upstream inherit wrong: %v", merged)
	}
}

func TestMergeVisibleSnapshots_Empty(t *testing.T) {
	merged := MergeVisibleSnapshots(nil)
	if len(merged) != 0 {
		t.Fatalf("expected empty map, got %v", merged)
	}
}

func TestMergeVisibleSnapshotsWithWriters(t *testing.T) {
	ups := []UpstreamSnapshot{
		{NodeID: "n1", Variables: map[string]string{"seed": "old", "out": "one"}, Writers: map[string]string{"out": "n1"}, LastAssistantMsg: "one"},
		{NodeID: "n2", Variables: map[string]string{"other": "two"}, Writers: map[string]string{"other": "n2"}, LastAssistantMsg: "two"},
	}
	merged, writers := MergeVisibleSnapshotsWithWriters(ups)
	if merged["seed"] != "old" || writers["seed"] != "" {
		t.Fatalf("seed should remain initial-sourced, merged=%v writers=%v", merged, writers)
	}
	if merged["out"] != "one" || writers["out"] != "n1" || merged["other"] != "two" || writers["other"] != "n2" {
		t.Fatalf("output writers not preserved, merged=%v writers=%v", merged, writers)
	}
	if merged[reservedLastAssistant] != "two" || writers[reservedLastAssistant] != "n2" {
		t.Fatalf("last assistant writer wrong, merged=%v writers=%v", merged, writers)
	}
}
