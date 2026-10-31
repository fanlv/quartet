package handler

import (
	"context"
	"strings"
	"time"

	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/types/agui"
)

type personalSummaryEvents struct {
	body strings.Builder
	err  error
}

func (h *personalSummaryEvents) OnMessageStart() error {
	if h.body.Len() > 0 {
		h.body.WriteString("\n")
	}
	return nil
}
func (h *personalSummaryEvents) OnMessageDelta(s string) error                      { h.body.WriteString(s); return nil }
func (*personalSummaryEvents) OnMessageEnd() error                                  { return nil }
func (*personalSummaryEvents) LastMessageID() string                                { return "" }
func (*personalSummaryEvents) OnThoughtStart() error                                { return nil }
func (*personalSummaryEvents) OnThoughtDelta(string) error                          { return nil }
func (*personalSummaryEvents) OnThoughtEnd() error                                  { return nil }
func (*personalSummaryEvents) OnToolCallStart(string, string) error                 { return nil }
func (*personalSummaryEvents) OnToolCallArgs(string, string, bool) error            { return nil }
func (*personalSummaryEvents) OnToolCallResult(string, string, bool) error          { return nil }
func (*personalSummaryEvents) OnToolCallEnd(string, bool) error                     { return nil }
func (*personalSummaryEvents) OnToolCallInterrupted(string, string) error           { return nil }
func (*personalSummaryEvents) OnToolCallStitched(string, string, bool, int64) error { return nil }
func (*personalSummaryEvents) OnTokenUsage(int, bool) error                         { return nil }
func (h *personalSummaryEvents) OnError(err error)                                  { h.err = err }

// Keep the existing usage recorder and its durable per-execution deduplication.
type personalUsageEvents struct {
	agui.EventHandler
	ctx                context.Context
	acc                *usagestats.Accumulator
	assistant, thought strings.Builder
}

func newPersonalUsageEvents(ctx context.Context, events agui.EventHandler) *personalUsageEvents {
	return &personalUsageEvents{EventHandler: events, ctx: ctx, acc: usagestats.NewAccumulator()}
}
func (h *personalUsageEvents) OnMessageStart() error {
	h.assistant.Reset()
	return h.EventHandler.OnMessageStart()
}
func (h *personalUsageEvents) OnMessageDelta(s string) error {
	h.assistant.WriteString(s)
	return h.EventHandler.OnMessageDelta(s)
}
func (h *personalUsageEvents) OnMessageEnd() error {
	h.acc.OnAssistantText(h.ctx, h.assistant.String())
	return h.EventHandler.OnMessageEnd()
}
func (h *personalUsageEvents) OnThoughtStart() error {
	h.thought.Reset()
	return h.EventHandler.OnThoughtStart()
}
func (h *personalUsageEvents) OnThoughtDelta(s string) error {
	h.thought.WriteString(s)
	return h.EventHandler.OnThoughtDelta(s)
}
func (h *personalUsageEvents) OnThoughtEnd() error {
	h.acc.OnThoughtText(h.ctx, h.thought.String())
	return h.EventHandler.OnThoughtEnd()
}
func (h *personalUsageEvents) OnToolCallStart(id, name string) error {
	h.acc.OnToolCallStart(id, name, time.Now().UnixMilli())
	return h.EventHandler.OnToolCallStart(id, name)
}
func (h *personalUsageEvents) OnToolCallArgs(id, args string, replace bool) error {
	if replace {
		h.acc.OnToolCallArgsSnapshot(id, args)
	} else {
		h.acc.OnToolCallArgsDelta(id, args)
	}
	return h.EventHandler.OnToolCallArgs(id, args, replace)
}
func (h *personalUsageEvents) OnToolCallEnd(id string, success bool) error {
	h.acc.OnToolCallEnd(h.ctx, id, time.Now().UnixMilli())
	return h.EventHandler.OnToolCallEnd(id, success)
}
func (h *personalUsageEvents) OnTokenUsage(n int, estimated bool) error {
	h.acc.SetEstimatedUsage(n)
	return h.EventHandler.OnTokenUsage(n, estimated)
}
func (h *personalUsageEvents) OnPromptUsage(u agui.PromptUsage) error {
	value := func(v *int64) int {
		if v == nil {
			return 0
		}
		return int(max(0, *v))
	}
	h.acc.SetProviderUsage(usagestats.ProviderTokenUsage{Total: int(max(0, u.TotalTokens)), Input: int(max(0, u.InputTokens)), Output: int(max(0, u.OutputTokens)), CachedRead: value(u.CachedReadTokens), CachedWrite: value(u.CachedWriteTokens), Reasoning: value(u.ThoughtTokens)})
	return nil
}
