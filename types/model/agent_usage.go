package model

// AgentUsageResponse is the envelope for GET /api/v1/agent/usage. Exactly one
// of Codex / Claude / Antigravity / Kimi / Qoder / Cursor / CodeBuddy is
// populated, matching the requested Type.
type AgentUsageResponse struct {
	Code        int               `json:"code"`
	Type        string            `json:"type"` // "codex" | "claude" | "antigravity" | "kimi" | "qoder" | "cursor" | "codebuddy"
	Codex       *CodexUsage       `json:"codex,omitempty"`
	Claude      *ClaudeUsage      `json:"claude,omitempty"`
	Antigravity *AntigravityUsage `json:"antigravity,omitempty"`
	Kimi        *KimiUsage        `json:"kimi,omitempty"`
	Qoder       *QoderUsage       `json:"qoder,omitempty"`
	Cursor      *CursorUsage      `json:"cursor,omitempty"`
	CodeBuddy   *CodeBuddyUsage   `json:"codebuddy,omitempty"`
}

// UsageWindow is one rate-limit window. LimitWindowSeconds is the source of
// truth for its duration; upstream field names do not imply a fixed period.
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
	Version         string       `json:"version,omitempty"` // effective Codex CLI version used by codex-acp, e.g. "v0.144.0"
	PrimaryWindow   *UsageWindow `json:"primary_window,omitempty"`
	SecondaryWindow *UsageWindow `json:"secondary_window,omitempty"`
	ResetCredits    int          `json:"reset_credits"` // count of available rate-limit reset credits
	// ResetCreditExpiries lists the expiry (unix seconds) of each available reset
	// credit, ascending. Sourced from the rate-limit-reset-credits endpoint;
	// empty when that supplementary call fails.
	ResetCreditExpiries []int64 `json:"reset_credit_expiries,omitempty"`
}

// AgentVersionResponse is the envelope for GET /api/v1/agent/version. It
// reports the installed CLI version of a built-in or custom ACP agent (e.g.
// "v1.17.18"). Version is empty when the binary advertises no parseable version.
type AgentVersionResponse struct {
	Code    int    `json:"code"`
	Version string `json:"version,omitempty"`
}

// ClaudeUsage is the current Claude Code subscription and rate-limit snapshot
// returned by Anthropic's OAuth usage endpoint.
type ClaudeUsage struct {
	PlanType      string                    `json:"plan_type,omitempty"`
	RateLimitTier string                    `json:"rate_limit_tier,omitempty"`
	Version       string                    `json:"version,omitempty"` // effective Claude Code version used by claude-agent-acp, e.g. "v2.1.202"
	FiveHour      *UsageWindow              `json:"five_hour,omitempty"`
	SevenDay      *UsageWindow              `json:"seven_day,omitempty"`
	SevenDayOpus  *UsageWindow              `json:"seven_day_opus,omitempty"`
	WeeklyScoped  []ClaudeScopedUsageWindow `json:"weekly_scoped,omitempty"`
	ExtraUsage    *ClaudeExtraUsage         `json:"extra_usage,omitempty"`
}

// ClaudeScopedUsageWindow is a model-specific weekly limit returned through
// the generic limits array (for example, Fable).
type ClaudeScopedUsageWindow struct {
	Label string `json:"label"`
	UsageWindow
}

// ClaudeExtraUsage describes the optional monthly pay-as-you-go allowance.
type ClaudeExtraUsage struct {
	Enabled      bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit,omitempty"`
	UsedCredits  *float64 `json:"used_credits,omitempty"`
	Currency     string   `json:"currency,omitempty"`
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

// KimiUsage is the Kimi Code plan snapshot from the kimi coding usages
// endpoint: the kimi CLI version plus the three quota windows. Weekly and
// FiveHour are rate-limit windows with a reset time; Total is the cumulative
// quota pool (e.g. purchased credits) and has no reset. A window is nil when
// the API does not report a usable limit for it.
type KimiUsage struct {
	Version       string       `json:"version,omitempty"`        // kimi CLI version, e.g. "v0.1.0"
	ParallelLimit int64        `json:"parallel_limit,omitempty"` // max concurrent sessions
	Weekly        *UsageWindow `json:"weekly,omitempty"`         // 7-day quota (the API's "usage" field)
	FiveHour      *UsageWindow `json:"five_hour,omitempty"`      // 5-hour quota (limits[0].detail)
	Total         *UsageWindow `json:"total,omitempty"`          // cumulative quota (totalQuota), no reset
}

// QoderUsage is the QoderCN credits quota snapshot from the openapi quota
// endpoint. Credits are a single cumulative pool (no rate-limit window reset);
// the pool expires wholesale at ExpiresAt (unix ms from the API, stored here as
// unix seconds). UsedPercent mirrors the API's totalUsagePercentage (0–100).
type QoderUsage struct {
	Version       string  `json:"version,omitempty"`    // qoderclicn CLI version, e.g. "v1.0.48"
	PlanType      string  `json:"plan_type,omitempty"`  // API userType, e.g. "personal_professional_trial"
	Unit          string  `json:"unit,omitempty"`       // quota unit, always "credits"
	Total         float64 `json:"total"`                // total credits in the pool
	Used          float64 `json:"used"`                 // credits consumed
	Remaining     float64 `json:"remaining"`            // credits left
	UsedPercent   float64 `json:"used_percent"`         // 0–100
	ExpiresAt     int64   `json:"expires_at,omitempty"` // unix seconds when the plan/quota expires
	QuotaExceeded bool    `json:"quota_exceeded"`       // true when the pool is exhausted
}

// CursorUsage is the Cursor plan snapshot from cursor.com's usage-summary
// endpoint plus the Cursor Bot (internal codename "Sand") usage RPC, using the
// cursor-agent CLI's locally stored login token. The three plan windows share
// the monthly billing cycle (reset at billingCycleEnd, LimitWindowSeconds =
// cycle length): PrimaryWindow is the plan total, SecondaryWindow the Auto
// lane, TertiaryWindow the API lane. GrokBotWindow is only reported when the
// account has Grok Bot access and the RPC returns a usable percent and reset
// time; otherwise it is nil. A window is nil when the API reports no usable
// percent for it.
type CursorUsage struct {
	Version         string       `json:"version,omitempty"`          // cursor-agent CLI version, e.g. "v2026.09.08"
	MembershipType  string       `json:"membership_type,omitempty"`  // e.g. "pro" | "team" | "enterprise"
	PrimaryWindow   *UsageWindow `json:"primary_window,omitempty"`   // plan total usage (Auto + API lanes)
	SecondaryWindow *UsageWindow `json:"secondary_window,omitempty"` // Auto lane usage
	TertiaryWindow  *UsageWindow `json:"tertiary_window,omitempty"`  // API lane usage
	GrokBotWindow   *UsageWindow `json:"grok_bot_window,omitempty"`  // Grok Bot usage, nil when unavailable
}

// CodeBuddyUsage is the CodeBuddy quota snapshot: the CodeBuddy CLI version,
// plus the monthly quota and the current calendar-month spend in CNY from the
// company Token 看板 OpenAPI (openapi.token.woa.com). QuotaText/CostText keep
// the API's raw string values; Quota is nil when the API reports a non-numeric
// quota ("-" — special states such as unlimited), in which case Remaining and
// UsedPercent are nil too and must not be derived numerically.
type CodeBuddyUsage struct {
	Version     string   `json:"version,omitempty"` // CodeBuddy CLI version, e.g. "v2.6.0"
	Username    string   `json:"username,omitempty"`
	QuotaText   string   `json:"quota_text"`
	CostText    string   `json:"cost_text"`
	Quota       *float64 `json:"quota,omitempty"`
	Cost        *float64 `json:"cost,omitempty"`
	Remaining   *float64 `json:"remaining,omitempty"`
	UsedPercent *float64 `json:"used_percent,omitempty"` // 0–100
}
