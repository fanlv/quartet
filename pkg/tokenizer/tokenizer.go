package tokenizer

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/msgextra"
	"github.com/tiktoken-go/tokenizer"
)

const tokenCountExtraKey = msgextra.KeyTokenCountCache

func MessagesTokenCounter(ctx context.Context, msgs []*schema.Message) int {
	var total int
	for _, msg := range msgs {
		total += MessageTokenCounter(ctx, msg)
	}
	return total
}

func MessageTokenCounter(ctx context.Context, msg *schema.Message) int {
	if msg == nil {
		return 0
	}

	if cached, ok := getCachedTokenCount(msg); ok {
		return cached
	}

	var sb strings.Builder
	sb.WriteString(string(msg.Role))
	sb.WriteString("\n")
	sb.WriteString(msg.ReasoningContent)
	sb.WriteString("\n")
	if msg.Content != "" {
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	} else {
		for _, content := range msg.UserInputMultiContent {
			sb.WriteString(content.Text)
			sb.WriteString("\n")
		}
	}
	if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			sb.WriteString(tc.Function.Name)
			sb.WriteString("\n")
			sb.WriteString(tc.Function.Arguments)
		}
	}

	n, err := estimateTokenCount(sb.String())
	if err != nil {
		n = fallbackEstimateTokenCount(sb.String())
	}
	setCachedTokenCount(msg, n)

	return n
}

func getCachedTokenCount(msg *schema.Message) (int, bool) {
	if msg == nil || msg.Extra == nil {
		return 0, false
	}
	v, ok := msg.Extra[tokenCountExtraKey]
	if !ok {
		return 0, false
	}
	switch vv := v.(type) {
	case int:
		return vv, true
	case int64:
		return int(vv), true
	case float64:
		return int(vv), true
	default:
		return 0, false
	}
}

func setCachedTokenCount(msg *schema.Message, n int) {
	setMessageExtra(msg, tokenCountExtraKey, n)
}

func setMessageExtra[T any](msg *schema.Message, k string, v T) {
	if msg == nil {
		return
	}
	if msg.Extra == nil {
		msg.Extra = map[string]any{}
	}

	newExtra := make(map[string]any, len(msg.Extra)+1)
	for kk, vv := range msg.Extra {
		newExtra[kk] = vv
	}

	newExtra[k] = v
	msg.Extra = newExtra
}

func estimateTokenCount(text string) (int, error) {
	if text == "" {
		return 0, nil
	}

	enc, err := tokenizer.ForModel(tokenizer.GPT4o)
	if err != nil {
		return 0, err
	}

	tokens, _, err := enc.Encode(text)
	if err != nil {
		return 0, err
	}

	return len(tokens), nil

}

func fallbackEstimateTokenCount(text string) int {
	return (len(text) + 3) / 4
}
