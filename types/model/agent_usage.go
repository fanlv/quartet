package model

// AgentUsageResponse is the envelope for GET /api/v1/agent/usage. Exactly one
// of Codex / Claude / Antigravity is populated, matching the requested Type.
type AgentUsageResponse struct {
	Code        int               `json:"code"`
	Type        string            `json:"type"` // "codex" | "claude" | "antigravity"
	Codex       *CodexUsage       `json:"codex,omitempty"`
	Claude      *ClaudeUsage      `json:"claude,omitempty"`
	Antigravity *AntigravityUsage `json:"antigravity,omitempty"`
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
	Version         string       `json:"version,omitempty"` // bundled codex CLI version, e.g. "v0.144.0"
	PrimaryWindow   *UsageWindow `json:"primary_window,omitempty"`   // 5-hour
	SecondaryWindow *UsageWindow `json:"secondary_window,omitempty"` // 7-day
	ResetCredits    int          `json:"reset_credits"`              // count of available rate-limit reset credits
	// ResetCreditExpiries lists the expiry (unix seconds) of each available reset
	// credit, ascending. Sourced from the rate-limit-reset-credits endpoint;
	// empty when that supplementary call fails.
	ResetCreditExpiries []int64 `json:"reset_credit_expiries,omitempty"`
}

// AgentVersionResponse is the envelope for GET /api/v1/agent/version. It
// reports the installed CLI version of a known ACP agent (e.g. "v1.17.18"),
// used by the composer usage strip for agents that have no quota view of their
// own (everything except Codex / Claude). Version is empty when the agent's
// binary advertises no parseable version.
type AgentVersionResponse struct {
	Code    int    `json:"code"`
	Version string `json:"version,omitempty"`
}

// ClaudeUsage is the current Claude key's spend in USD (today + total).
type ClaudeUsage struct {
	Name      string  `json:"name,omitempty"`
	KeySuffix string  `json:"key_suffix,omitempty"`
	Version   string  `json:"version,omitempty"` // claude-agent-acp version, e.g. "v2.1.202"
	TodayCost float64 `json:"today_cost"`
	TotalCost float64 `json:"total_cost"`
}

// AntigravityUsage is the Antigravity (agy) built-in plan snapshot: the agy CLI
// version plus the two model groups' quota windows, each with a 7-day (weekly)
// and a 5-hour bucket. Each window reuses UsageWindow — its UsedPercent is
// derived from the API's remaining fraction, and ResetAt from the bucket's
// reset time. A window is nil when the corresponding bucket is absent.
type AntigravityUsage struct {
	Version      string       `json:"version,omitempty"`       // agy CLI version, e.g. "v1.1.1"
	ClaudeWeekly *UsageWindow `json:"claude_weekly,omitempty"` // Claude/GPT group, 7-day  (bucketId 3p-weekly)
	Claude5h     *UsageWindow `json:"claude_5h,omitempty"`     // Claude/GPT group, 5-hour (bucketId 3p-5h)
	GeminiWeekly *UsageWindow `json:"gemini_weekly,omitempty"` // Gemini group, 7-day  (bucketId gemini-weekly)
	Gemini5h     *UsageWindow `json:"gemini_5h,omitempty"`     // Gemini group, 5-hour (bucketId gemini-5h)
}
