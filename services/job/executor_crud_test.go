package job

import (
	"testing"

	"github.com/fanlv/quartet/types/model"
)

func TestSummarizePreservesInitialInteractiveConfiguration(t *testing.T) {
	job := model.NewJob("/workspace", "ws-1")
	job.InitialAgentID = "claude"
	job.FirstModelID = "claude-sonnet"
	job.InitialACPMode = "plan"
	job.InitialACPThoughtLevel = "high"

	summary := summarize(job)
	if summary.AgentID != job.InitialAgentID {
		t.Fatalf("AgentID = %q, want %q", summary.AgentID, job.InitialAgentID)
	}
	if summary.ModelID != job.FirstModelID {
		t.Fatalf("ModelID = %q, want %q", summary.ModelID, job.FirstModelID)
	}
	if summary.ACPMode != job.InitialACPMode {
		t.Fatalf("ACPMode = %q, want %q", summary.ACPMode, job.InitialACPMode)
	}
	if summary.ACPThoughtLevel != job.InitialACPThoughtLevel {
		t.Fatalf("ACPThoughtLevel = %q, want %q", summary.ACPThoughtLevel, job.InitialACPThoughtLevel)
	}
}
