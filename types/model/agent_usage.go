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
