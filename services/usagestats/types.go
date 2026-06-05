// Package usagestats records and queries per-step Quartet usage statistics.
//
// Data is bucketed by (workspace, day, model) with secondary breakdowns by
// activity kind (assistant / thought / toolcall) and by tool command name.
// Each step / turn produces one Snapshot delivered to the Recorder; the
// Reader exposes day-level totals (for the Job List header) and range-level
// aggregates (for the stats page) by reading the same monthly-sharded JSON
// files on disk.
//
// All token counts in this package are local tokenizer estimates, NOT
// API-billable values. See the feature design doc for details.
package usagestats

// SectionTotals is the value that lives at every aggregation node:
// the day-level total bucket, every per-model subbucket, every per-tool
// subbucket of the per-day or per-model bucket.
//
// Fields are accumulated independently by the Recorder; no field is derived
// from another at write time. Counts and totals are never decremented.
type SectionTotals struct {
	TotalMs        int64       `json:"totalMs"`
	TurnCount      int         `json:"turnCount"`
	AssistantCount int         `json:"assistantCount"`
	ThoughtCount   int         `json:"thoughtCount"`
	ToolCallCount  int         `json:"toolCallCount"`
	Tokens         TokenTotals `json:"tokens"`
}

// TokenTotals aggregates per-segment token estimates. Total is sourced from
// OnTokenUsage (whole-round estimate); the per-segment fields are summed
// from the per-message tokenizer cache. The two sums are NOT required to
// match (Total covers the whole history including user / tool_result /
// system, while the per-segment fields only cover this step's output).
type TokenTotals struct {
	Total     int `json:"total"`
	Assistant int `json:"assistant"`
	Thought   int `json:"thought"`
	ToolCall  int `json:"toolCall"`
}

// ToolBucket is the value of one entry in the `tools` map. We deliberately
// do not split tokens by tool — the design keeps token attribution at the
// day / model level only.
type ToolBucket struct {
	Count   int   `json:"count"`
	TotalMs int64 `json:"totalMs"`
}

// ModelBucket is one entry in the `models` map nested under a DayBucket.
// It carries the same field set as the day-level total plus its own tools
// breakdown, so a UI showing "what did opus do today on this workspace?"
// reads from a single struct.
type ModelBucket struct {
	SectionTotals
	Tools map[string]*ToolBucket `json:"tools,omitempty"`
}

// DayBucket is the value at workspace[YYYY-MM-DD]. Contains the day total,
// the per-model breakdown, and the day-level per-tool breakdown (sums over
// all models for that day, used by the By Tool view).
type DayBucket struct {
	SectionTotals
	Tools  map[string]*ToolBucket  `json:"tools,omitempty"`
	Models map[string]*ModelBucket `json:"models,omitempty"`
}

// MonthFile is the on-disk shape of one month-sharded JSON file. The outer
// key is workspaceId, then YYYY-MM-DD. Days outside the file's month never
// appear: cross-month writes naturally split into two file mutations.
type MonthFile struct {
	Workspaces map[string]map[string]*DayBucket `json:"workspaces"`
}

// Snapshot is the unit the Recorder consumes — one step's worth of usage.
// All fields are filled by the caller (typically the loop_event_handler
// Accumulator); the SDK does not synthesise any field from any other.
type Snapshot struct {
	WorkspaceID    string
	ModelID        string
	FinishedAtMs   int64
	DurationMs     int64
	AssistantCount int
	ThoughtCount   int
	ToolCallCount  int
	Tokens         TokenTotals
	Tools          map[string]ToolBucket
}
