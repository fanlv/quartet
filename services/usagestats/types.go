// Package usagestats records and queries per-step Quartet usage statistics.
//
// Data is bucketed by (workspace, day, model) with secondary breakdowns by
// activity kind (assistant / thought / toolcall) and by tool command name.
// Each step / turn produces one Snapshot delivered to the Recorder; the
// Reader exposes day-level totals (for the Job List header) and range-level
// aggregates (for the stats page) by reading the same monthly-sharded JSON
// files on disk.
//
// Provider-reported token counts and local tokenizer estimates are stored in
// separate fields. Callers must not interpret Estimated as an API-billable
// value.
package usagestats

// currentSchemaVersion is the only on-disk layout this build reads or writes.
// A month file carrying any other version is rejected rather than upgraded:
// converting stored data is an operational task, not a runtime one.
const currentSchemaVersion = 3

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

// TokenTotals keeps provider-reported consumption separate from local
// estimates. Total is the API-facing aggregate across all recorded turns;
// Reported and Estimated explain its composition. Input, Output, cached
// read/write, and Reasoning are provider-reported details. ImageEstimate is a
// descriptive subset already included in provider input or the estimated total,
// never an additional contribution to Total. Assistant/Thought/ToolCall remain
// local output-segment estimates.
//
// ReportedTurns and EstimatedTurns are coverage counters, not token counts.
// Every recorded turn contributes to exactly one of them, so
// ReportedTurns+EstimatedTurns always equals TurnCount for a bucket. A Graph
// turn with mixed retry sources is conservatively classified as estimated.
type TokenTotals struct {
	Total          int `json:"total"`
	Reported       int `json:"reported"`
	Input          int `json:"input"`
	Output         int `json:"output"`
	CachedRead     int `json:"cachedRead"`
	CachedWrite    int `json:"cachedWrite"`
	Reasoning      int `json:"reasoning"`
	ImageEstimate  int `json:"imageEstimate"`
	Estimated      int `json:"estimated"`
	ReportedTurns  int `json:"reportedTurns"`
	EstimatedTurns int `json:"estimatedTurns"`
	Assistant      int `json:"assistant"`
	Thought        int `json:"thought"`
	ToolCall       int `json:"toolCall"`
}

// ProviderTokenUsage is one turn's authoritative provider usage. Values are
// kept verbatim; Total is not derived because provider accounting semantics can
// differ and the reported value is the source of truth.
type ProviderTokenUsage struct {
	Total       int
	Input       int
	Output      int
	CachedRead  int
	CachedWrite int
	Reasoning   int
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
	SchemaVersion int                              `json:"schemaVersion"`
	AppliedEvents map[string]bool                  `json:"appliedEvents,omitempty"`
	Workspaces    map[string]map[string]*DayBucket `json:"workspaces"`
}

// Snapshot is the unit the Recorder consumes — one step's worth of usage.
// All fields are filled by the caller (typically the loop_event_handler
// Accumulator); the SDK does not synthesise any field from any other.
type Snapshot struct {
	// EventID is a stable, globally unique completion identifier. Re-recording
	// the same ID in its month is a no-op, which is what makes retries and
	// crash-recovery replays safe. Snapshots without one are dropped.
	EventID        string
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
