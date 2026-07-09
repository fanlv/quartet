package model

// AgentUsageResponse is the envelope for GET /api/v1/agent/usage. Exactly one
// of Codex / Claude is populated, matching the requested Type.
type AgentUsageResponse struct {
	Code   int          `json:"code"`
	Type   string       `json:"type"` // "codex" | "claude"
	Codex  *CodexUsage  `json:"codex,omitempty"`
	Claude *ClaudeUsage `json:"claude,omitempty"`
}

// UsageWindow is one Codex rate-limit window (5-hour primary / 7-day secondary).
type UsageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"` // unix seconds
}

// CodexUsage is the Codex/ChatGPT plan + rate-limit snapshot pulled from the
// ChatGPT usage endpoint.
type CodexUsage struct {
	Email           string       `json:"email,omitempty"`
	PlanType        string       `json:"plan_type,omitempty"`
	Version         string       `json:"version,omitempty"` // codex-acp version, e.g. "v1.1.0"
	PrimaryWindow   *UsageWindow `json:"primary_window,omitempty"`   // 5-hour
	SecondaryWindow *UsageWindow `json:"secondary_window,omitempty"` // 7-day
	ResetCredits    int          `json:"reset_credits"`              // rate_limit_reset_credits.available_count
}

// ClaudeUsage is the current Claude key's spend in USD (today + total).
type ClaudeUsage struct {
	Name      string  `json:"name,omitempty"`
	KeySuffix string  `json:"key_suffix,omitempty"`
	Version   string  `json:"version,omitempty"` // claude-agent-acp version, e.g. "v2.1.202"
	TodayCost float64 `json:"today_cost"`
	TotalCost float64 `json:"total_cost"`
}
