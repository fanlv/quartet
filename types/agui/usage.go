package agui

// PromptUsage is the provider-reported token consumption for one completed
// prompt invocation. Unlike OnTokenUsage, which is a live context-window
// gauge, these values are suitable for additive usage statistics. Optional ACP
// categories use pointers so an absent value is not confused with a reported
// zero.
type PromptUsage struct {
	InputTokens       int64
	OutputTokens      int64
	TotalTokens       int64
	CachedReadTokens  *int64
	CachedWriteTokens *int64
	ThoughtTokens     *int64
}

// PromptUsageHandler is an optional extension implemented by consumers that
// need end-of-prompt accounting. It is deliberately not embedded in
// EventHandler so existing runners and lightweight test handlers remain
// source-compatible when an Agent does not expose terminal usage.
type PromptUsageHandler interface {
	OnPromptUsage(PromptUsage) error
}
