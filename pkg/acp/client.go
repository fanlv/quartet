package acp

import (
	"context"
	"strings"
	"sync"
	"time"

	acp "github.com/eino-contrib/acp"

	"github.com/fanlv/quartet/pkg/json"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/agentstream"
)

// registeredHandler wraps agentstream.StreamHandler with a per-session
// generation counter so a finishing Run that installed an earlier handler
// can distinguish "this handler is still mine" from "a newer Run has
// replaced it".
type registeredHandler struct {
	h   agentstream.StreamHandler
	gen uint64
}

// sdkClient implements acp.Client. It keeps a per-session registry of
// stream handlers, translates session/update notifications into
// StreamHandler calls, auto-approves permission requests, and returns
// stubs for file and terminal operations. Embedding acp.BaseClient
// supplies the default method-not-supported responses for the parts we
// don't override.
type sdkClient struct {
	acp.BaseClient

	autoApprove bool
	onActivity  func()

	mu       sync.Mutex
	handlers map[acp.SessionID]*registeredHandler
}

var _ acp.Client = (*sdkClient)(nil)

func newSDKClient() *sdkClient {
	return &sdkClient{
		autoApprove: true,
		handlers:    make(map[acp.SessionID]*registeredHandler),
	}
}

// SetStreamHandler installs the handler for the given session and returns
// a generation number. Pair with ClearStreamHandlerIfGen so a finishing
// Run cannot clear a newer Run's handler.
func (c *Conn) SetStreamHandler(sid acp.SessionID, h agentstream.StreamHandler) uint64 {
	c.client.mu.Lock()
	defer c.client.mu.Unlock()
	existing, ok := c.client.handlers[sid]
	if !ok {
		existing = &registeredHandler{}
		c.client.handlers[sid] = existing
	}
	existing.h = h
	existing.gen++
	return existing.gen
}

// ClearStreamHandlerIfGen clears the handler only if the generation still
// matches, avoiding a race where a finishing Run clears a newer Run's
// handler.
func (c *Conn) ClearStreamHandlerIfGen(sid acp.SessionID, gen uint64) {
	c.client.mu.Lock()
	defer c.client.mu.Unlock()
	existing, ok := c.client.handlers[sid]
	if !ok {
		return
	}
	if existing.gen == gen {
		existing.h = nil
	}
}

// RemoveSession drops the session's registry entry entirely. Call when the
// session is closed or the owning agent is torn down.
func (c *Conn) RemoveSession(sid acp.SessionID) {
	c.client.mu.Lock()
	delete(c.client.handlers, sid)
	c.client.mu.Unlock()
}

func (c *sdkClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	logger.Debugf(ctx, "[ACP][RequestPermission] options=%d", len(params.Options))
	if c.autoApprove {
		// Prefer AllowAlways to avoid repeated prompts; fall back to AllowOnce.
		var fallbackOptionID acp.PermissionOptionID
		for _, o := range params.Options {
			if o.Kind == acp.PermissionOptionKindAllowAlways {
				return acp.RequestPermissionResponse{
					Outcome: acp.NewRequestPermissionOutcomeSelected(acp.SelectedPermissionOutcome{OptionID: o.OptionID}),
				}, nil
			}
			if o.Kind == acp.PermissionOptionKindAllowOnce && fallbackOptionID == "" {
				fallbackOptionID = o.OptionID
			}
		}
		if fallbackOptionID != "" {
			return acp.RequestPermissionResponse{
				Outcome: acp.NewRequestPermissionOutcomeSelected(acp.SelectedPermissionOutcome{OptionID: fallbackOptionID}),
			}, nil
		}
		if len(params.Options) > 0 {
			return acp.RequestPermissionResponse{
				Outcome: acp.NewRequestPermissionOutcomeSelected(acp.SelectedPermissionOutcome{OptionID: params.Options[0].OptionID}),
			}, nil
		}
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.NewRequestPermissionOutcomeCancelled(acp.RequestPermissionOutcomeCancelled{}),
	}, nil
}

// SessionUpdate is called by the SDK for each streaming notification. The
// ClientConnection routes session/update through an ordered queue, so our
// callbacks are guaranteed to run in pipe order.
func (c *sdkClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	start := time.Now()
	c.handleSessionUpdate(ctx, params)
	elapsed := time.Since(start)
	// Warn when a single SessionUpdate dispatch takes too long — this blocks
	// the SDK's ordered queue and delays all subsequent events for this session.
	if elapsed > 500*time.Millisecond {
		logger.Warnf(ctx, "[ACP] SessionUpdate handler slow: sid=%s elapsed=%v", params.SessionID, elapsed)
	}
	return nil
}

func (c *sdkClient) handleSessionUpdate(ctx context.Context, params acp.SessionNotification) {
	if c.onActivity != nil {
		c.onActivity()
	}

	sid := params.SessionID
	c.mu.Lock()
	rh, ok := c.handlers[sid]
	var h agentstream.StreamHandler
	if ok {
		h = rh.h
	}
	c.mu.Unlock()
	if h == nil {
		return
	}

	u := params.Update
	if v, ok := u.AsAgentMessageChunk(); ok {
		if text, ok := v.Content.AsText(); ok {
			h.OnMessageChunk(text.Text)
		}
		return
	}
	if v, ok := u.AsAgentThoughtChunk(); ok {
		if text, ok := v.Content.AsText(); ok {
			h.OnThoughtChunk(text.Text)
		}
		return
	}
	if tc, ok := u.AsToolCall(); ok {
		h.OnToolCall(string(tc.ToolCallID), tc.Title)
		return
	}
	if tcu, ok := u.AsToolCallUpdate(); ok {
		id := string(tcu.ToolCallID)
		content := extractToolCallContent(tcu.Content)
		status := agentstream.ToolCallStatusInProgress
		if s := tcu.Status; s != nil {
			switch *s {
			case acp.ToolCallStatusCompleted:
				status = agentstream.ToolCallStatusCompleted
			case acp.ToolCallStatusFailed:
				status = agentstream.ToolCallStatusFailed
			case acp.ToolCallStatusPending:
				status = agentstream.ToolCallStatusPending
			case acp.ToolCallStatusInProgress:
				status = agentstream.ToolCallStatusInProgress
			}
		}
		// Log terminal tool call updates at Debug level to help trace delivery
		// latency (the builder's "late terminal stitch" uses these timestamps).
		if status == agentstream.ToolCallStatusCompleted || status == agentstream.ToolCallStatusFailed {
			logger.Debugf(ctx, "[ACP] tool_call_terminal: sid=%s toolID=%s status=%s contentLen=%d",
				sid, id, status, len(content))
		}
		h.OnToolCallUpdate(id, content, status)
		return
	}
	if usage, ok := u.AsUsageUpdate(); ok {
		if usage.Used > 0 {
			h.OnTokenUsage(int(usage.Used))
			// Temporarily logged at Info to confirm whether the subprocess
			// (e.g. coco / claude-agent-acp) actually emits usage_update
			// during long turns — the symptom under investigation is a
			// frozen token counter on a single multi-minute round.
			logger.Infof(ctx, "[ACP] usage_update: %d", int(usage.Used))
		}
		return
	}

	// Unknown / ACP-specific session update kinds stay internal to this
	// dispatcher. The public StreamHandler contract is protocol-neutral and
	// does not surface these; we only log.
	logUnknownSessionUpdate(ctx, &u)
}

// logUnknownSessionUpdate emits a debug log entry for session updates that
// the dispatcher does not translate to a StreamHandler call (plan,
// available commands, mode updates, etc.). Kept here so pkg/acp remains
// the sole owner of "this is an ACP-specific event" knowledge.
func logUnknownSessionUpdate(ctx context.Context, u *acp.SessionUpdate) {
	if v, ok := u.AsUserMessageChunk(); ok {
		logger.Debugf(ctx, "[ACP] user_message_chunk: %s", json.String(v))
		return
	}
	if v, ok := u.AsPlan(); ok {
		logger.Debugf(ctx, "[ACP] plan: %s", json.String(v))
		return
	}
	if v, ok := u.AsAvailableCommandsUpdate(); ok {
		logger.Debugf(ctx, "[ACP] available_commands_update: %s", json.String(v))
		return
	}
	if v, ok := u.AsCurrentModeUpdate(); ok {
		logger.Debugf(ctx, "[ACP] current_mode_update: %s", json.String(v))
		return
	}
	if v, ok := u.AsConfigOptionUpdate(); ok {
		logger.Debugf(ctx, "[ACP] config_option_update: %s", json.String(v))
		return
	}
	if v, ok := u.AsSessionInfoUpdate(); ok {
		logger.Debugf(ctx, "[ACP] session_info_update: %s", json.String(v))
		return
	}
	logger.Warnf(ctx, "[ACP] unknown session update: %s", json.String(*u))
}

func extractToolCallContent(contents []acp.ToolCallContent) string {
	var parts []string
	for _, c := range contents {
		cc, ok := c.AsContent()
		if !ok {
			continue
		}
		// cc embeds acp.Content by type, so cc.Content is the acp.Content
		// struct and cc.Content.Content is the ContentBlock.
		if text, ok := cc.Content.Content.AsText(); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
