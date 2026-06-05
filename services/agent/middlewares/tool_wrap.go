package middlewares

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/json"
	"github.com/fanlv/quartet/pkg/logger"
)

// isUnrecoverableToolError returns true for errors the LLM cannot meaningfully
// recover from by retrying with different arguments — currently context
// cancellation and deadline expiry. The default tool-wrap behavior is to fold
// errors into the result text so the model self-corrects on the next turn,
// but cancel/deadline are signals from the caller (user stop, request timeout)
// that the whole Run should unwind, not a tool-shaped problem the model
// should attempt to work around.
func isUnrecoverableToolError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type toolWrapMiddleware struct {
	adk.BaseChatModelAgentMiddleware
	// failures, when non-nil, is a shared registry of tool_call ids whose
	// underlying tool endpoint returned an error. The middleware still
	// returns the error text as a successful-looking result so the LLM
	// can self-recover on the next turn, but records the id here so the
	// eino round adapter can surface the failure to live UI and on-disk
	// history via ToolCallStatusFailed. Nil in contexts that don't wire
	// the registry (unit tests): failure tagging degrades gracefully.
	failures *sync.Map
}

func NewToolWrapMiddleware(failures *sync.Map) adk.ChatModelAgentMiddleware {
	return &toolWrapMiddleware{failures: failures}
}

func (b *toolWrapMiddleware) recordFailure(callID string) {
	if b.failures == nil || callID == "" {
		return
	}
	b.failures.Store(callID, struct{}{})
}

type toolWrapBaseModel struct {
	inner model.BaseChatModel
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(rs[:max-1]) + "…"
}

func printInputMessagesIfContains(ctx context.Context, stage string, input []*schema.Message) {
	// json.String(msg) is expensive on long inputs (full multi-turn context),
	// and Go evaluates Debugf's arguments before the call — so the level guard
	// inside logger can't short-circuit this work. Gate the whole loop here.
	if !logger.DebugEnabled() {
		return
	}
	for idx, msg := range input {
		if msg == nil {
			continue
		}
		ok := strings.Contains(msg.Content, "system-reminder")
		if ok {
			// system-reminder messages are larger but rarer; capture them at
			// Debug with a 2k cap so they don't drown the feed in production.
			logger.Debugf(ctx, "[llm/%s] input(%d) [system-reminder]:\n%s", stage, idx, truncateRunes(msg.Content, 2000))
			continue
		}
		logger.Debugf(ctx, "[llm/%s] input(%d):\n%s", stage, idx, truncateRunes(json.String(msg), 200))
	}
}

func (w *toolWrapBaseModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	printInputMessagesIfContains(ctx, "Generate", input)
	return w.inner.Generate(ctx, input, opts...)
}

func (w *toolWrapBaseModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	printInputMessagesIfContains(ctx, "Stream", input)
	return w.inner.Stream(ctx, input, opts...)
}

func (b *toolWrapMiddleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			logger.Warnf(ctx, "[tool] %s (call=%s) failed: %v", tCtx.Name, tCtx.CallID, err)
			// Skip the failure-registry write for unrecoverable errors.
			// Those return early before producing a tool terminal, and
			// the round adapter only consumes failures via LoadAndDelete
			// at terminal time — recording here would leak the entry in
			// the agent-level sync.Map indefinitely. Frequent Stop /
			// timeout traffic on a long-lived agent would accumulate
			// dormant entries in toolFailures. Logging still fires so
			// operators see the failure regardless.
			if isUnrecoverableToolError(err) {
				return "", err
			}
			b.recordFailure(tCtx.CallID)
			return err.Error(), nil
		}
		return result, nil
	}, nil
}

func (b *toolWrapMiddleware) WrapStreamableToolCall(ctx context.Context, endpoint adk.StreamableToolCallEndpoint,
	tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		result, err := endpoint(ctx, argumentsInJSON, opts...)

		if err != nil {
			// Symmetry with the invokable path: return the error text to the
			// model so it can self-recover, but record the failure so the
			// eino round adapter can surface it as a failed tool result to
			// live UI + on-disk history (which otherwise would render the
			// error text as a green "Completed" bubble). Unrecoverable
			// errors skip the registry write — they return before any
			// terminal is produced, and a recorded callID would leak
			// because nothing will ever LoadAndDelete it.
			logger.Warnf(ctx, "[tool] %s (call=%s) streamable failed: %v", tCtx.Name, tCtx.CallID, err)
			if isUnrecoverableToolError(err) {
				return nil, err
			}
			b.recordFailure(tCtx.CallID)
			return schema.StreamReaderFromArray([]string{err.Error()}), nil
		}

		return result, nil
	}, nil
}

func (b *toolWrapMiddleware) WrapModel(_ context.Context, m model.BaseChatModel, _ *adk.ModelContext) (model.BaseChatModel, error) {
	return &toolWrapBaseModel{inner: m}, nil
}
