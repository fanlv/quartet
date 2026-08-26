package runtime

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	template "github.com/cloudwego/eino/utils/callbacks"
)

// ProviderUsage is the provider-reported token usage for one Agent Run. It is
// deliberately separate from the context-window usage_update notifications:
// those are local tokenizer estimates over persisted history, while these are
// the authoritative counters returned by the model provider for this turn.
type ProviderUsage struct {
	InputTokens      int64
	OutputTokens     int64
	TotalTokens      int64
	CachedReadTokens int64
	ThoughtTokens    int64
}

type providerUsageCollector struct {
	mu sync.Mutex

	total  ProviderUsage
	seen   bool
	sealed bool
}

func newProviderUsageCollector() *providerUsageCollector {
	return &providerUsageCollector{}
}

// handler is installed with adk.WithCallbacks for one Run only. Each direct
// model response contributes one request. Streaming model usage is collected
// from the AgentEvent stream that the runtime already consumes; intentionally
// omitting OnEndWithStreamOutput prevents Eino from creating an extra stream
// copy that could either retain memory or block forever on a broken provider.
func (c *providerUsageCollector) handler() callbacks.Handler {
	return template.NewHandlerHelper().ChatModel(&template.ModelCallbackHandler{
		OnEnd: func(ctx context.Context, _ *callbacks.RunInfo, output *model.CallbackOutput) context.Context {
			c.add(callbackOutputUsage(output))
			return ctx
		},
	}).Handler()
}

func callbackOutputUsage(output *model.CallbackOutput) *ProviderUsage {
	if output == nil {
		return nil
	}
	usage := providerUsageFromModel(output.TokenUsage)
	if output.Message != nil && output.Message.ResponseMeta != nil {
		usage = maxProviderUsage(usage, providerUsageFromSchema(output.Message.ResponseMeta.Usage))
	}
	return usage
}

func providerUsageFromModel(usage *model.TokenUsage) *ProviderUsage {
	if usage == nil {
		return nil
	}
	return &ProviderUsage{
		InputTokens:      int64(usage.PromptTokens),
		OutputTokens:     int64(usage.CompletionTokens),
		TotalTokens:      int64(usage.TotalTokens),
		CachedReadTokens: int64(usage.PromptTokenDetails.CachedTokens),
		ThoughtTokens:    int64(usage.CompletionTokensDetails.ReasoningTokens),
	}
}

func providerUsageFromSchema(usage *schema.TokenUsage) *ProviderUsage {
	if usage == nil {
		return nil
	}
	return &ProviderUsage{
		InputTokens:      int64(usage.PromptTokens),
		OutputTokens:     int64(usage.CompletionTokens),
		TotalTokens:      int64(usage.TotalTokens),
		CachedReadTokens: int64(usage.PromptTokenDetails.CachedTokens),
		ThoughtTokens:    int64(usage.CompletionTokensDetails.ReasoningTokens),
	}
}

func maxProviderUsage(current, next *ProviderUsage) *ProviderUsage {
	if next == nil {
		return current
	}
	if current == nil {
		copy := *next
		return &copy
	}
	result := *current
	result.InputTokens = max(current.InputTokens, next.InputTokens)
	result.OutputTokens = max(current.OutputTokens, next.OutputTokens)
	result.TotalTokens = max(current.TotalTokens, next.TotalTokens)
	result.CachedReadTokens = max(current.CachedReadTokens, next.CachedReadTokens)
	result.ThoughtTokens = max(current.ThoughtTokens, next.ThoughtTokens)
	return &result
}

func (c *providerUsageCollector) add(usage *ProviderUsage) {
	if usage == nil {
		return
	}
	c.mu.Lock()
	if c.sealed {
		c.mu.Unlock()
		return
	}
	c.total.InputTokens += usage.InputTokens
	c.total.OutputTokens += usage.OutputTokens
	c.total.TotalTokens += usage.TotalTokens
	c.total.CachedReadTokens += usage.CachedReadTokens
	c.total.ThoughtTokens += usage.ThoughtTokens
	c.seen = true
	c.mu.Unlock()
}

// finish seals the collector after the synchronous event loop and all direct
// callbacks have returned. Streaming collection happens inline in
// roundAdapter.forwardStream.
func (c *providerUsageCollector) finish() {
	c.mu.Lock()
	c.sealed = true
	c.mu.Unlock()
}

func (c *providerUsageCollector) snapshot() *ProviderUsage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.seen {
		return nil
	}
	usage := c.total
	return &usage
}
