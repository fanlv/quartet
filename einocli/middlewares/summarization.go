package middlewares

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/einocli/logger"
	"github.com/fanlv/quartet/einocli/store"
	"github.com/fanlv/quartet/einocli/types/msgextra"
)

// defaultSummarizationTokenThreshold is tuned for models with ~200k context
// windows, leaving headroom for the current turn. Override per-deployment via
// EINO_CLI_SUMMARIZATION_TOKEN_THRESHOLD — e.g. 900_000 on 1M-context models,
// 90_000 on 128k-context models. Hardcoding a single number meant smaller
// models would trip context-window errors before summarization ever fired.
const (
	defaultSummarizationTokenThreshold = 190_000
	envSummarizationTokenThreshold     = "EINO_CLI_SUMMARIZATION_TOKEN_THRESHOLD"
)

func summarizationTokenThreshold() int {
	raw := os.Getenv(envSummarizationTokenThreshold)
	if raw == "" {
		return defaultSummarizationTokenThreshold
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return defaultSummarizationTokenThreshold
	}
	return v
}

func NewSummarizationMW(ctx context.Context, chatModel model.BaseChatModel, repo store.ChatContextRepo) (adk.ChatModelAgentMiddleware, error) {
	threshold := summarizationTokenThreshold()
	mw, err := summarization.New(ctx, &summarization.Config{
		Model: chatModel,
		Trigger: &summarization.TriggerCondition{
			ContextTokens: threshold,
		},
		EmitInternalEvents: true,
		Finalize: func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
			// Determine the summary index = how many persisted messages this
			// summary now covers.
			//
			// originalMessages layout depends on whether LoadMessagesForLLM
			// injected a prior summary this round:
			//   - first summarization:   [sys, msg1, msg2, ...]
			//   - subsequent summarizations when old summary was injected:
			//     [sys, old_summary, msg1, msg2, ...]
			//   - subsequent summarizations when the summary read failed
			//     upstream and LoadMessagesForLLM degraded to no-summary:
			//     [sys, msg1, msg2, ...] (same shape as "first")
			//
			// We must NOT infer "old summary is in originalMessages" by
			// re-reading summary.json here — a transient read failure at
			// LoadMessagesForLLM + success here would silently subtract 1
			// and leave summary.index off by one, which then propagates
			// into orphan-tail scans that use summary.index as their
			// scan lower bound. Instead we check the authoritative signal:
			// LoadMessagesForLLM marks the injected summary message with
			// msgextra.KeyIsSummary. If no message in originalMessages
			// carries that marker, no old summary was injected.
			oldSummaryInjected := false
			for _, msg := range originalMessages {
				if msg == nil || msg.Extra == nil {
					continue
				}
				if v, ok := msg.Extra[msgextra.KeyIsSummary].(bool); ok && v {
					oldSummaryInjected = true
					break
				}
			}

			var newMsgCount int
			for _, msg := range originalMessages {
				if msg == nil {
					continue
				}
				if msg.Role != schema.System {
					newMsgCount++
				}
			}

			var index int
			if oldSummaryInjected {
				// Read prevSummary only for its Index. If the read fails
				// here we cannot compute the correct new index (we'd be
				// guessing from 0), so fail the finalize: the caller will
				// keep originalMessages uncompressed for this round and
				// try again next round rather than persist a wrong index.
				prevSummary, err := repo.LoadSummaryMessage(ctx)
				if err != nil {
					logger.Errorf(ctx, "load previous summary failed while old summary is present in originalMessages: %v", err)
					return originalMessages, nil
				}
				if prevSummary == nil {
					// Inconsistency: we saw a summary marker in
					// originalMessages but summary.json is now empty.
					// Bail out rather than persist a guess.
					logger.Errorf(ctx, "summary marker present in originalMessages but summary.json now empty; skipping this summarization")
					return originalMessages, nil
				}
				// Old summary itself is one of the counted messages;
				// subtract it so index only grows by the count of
				// genuinely new persisted messages.
				index = prevSummary.Index + newMsgCount - 1
			} else {
				index = newMsgCount
			}

			err := repo.SaveSummaryMessage(ctx, &store.SummaryMessage{
				Index:   index,
				Message: summary,
			})
			if err != nil {
				logger.Errorf(ctx, "save summary message failed, skipping summarization: %v", err)
				return originalMessages, nil
			}
			logger.Infof(ctx, "saved summary message, index=%d (oldSummaryInjected=%v, newMsgCount=%d)", index, oldSummaryInjected, newMsgCount)

			// Preserve default behavior: system messages + summary
			var result []adk.Message
			for _, msg := range originalMessages {
				if msg == nil {
					continue
				}
				if msg.Role == schema.System {
					result = append(result, msg)
				}
			}
			result = append(result, summary)
			return result, nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create summarization middleware: %w", err)
	}
	return mw, nil
}
