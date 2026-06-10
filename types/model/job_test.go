package model

import (
	"testing"
)

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
