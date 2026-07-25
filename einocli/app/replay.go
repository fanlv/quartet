package app

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/schema"
	acp "github.com/eino-contrib/acp"
	"github.com/fanlv/quartet/einocli/logger"
	"github.com/fanlv/quartet/einocli/store"
	"github.com/fanlv/quartet/einocli/types/msgextra"
)

// LoadSession restores a session and replays its persisted history as
// session/update notifications before responding. The replay is emitted from
// inside the handler, and the stdio transport serializes writes, so every
// replayed update reaches the client before the LoadSession response.
func (a *Agent) LoadSession(ctx context.Context, req acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	st, err := a.getOrLoadState(string(req.SessionID))
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}

	a.replayHistory(ctx, st)

	opts, err := a.configOptions(st)
	if err != nil {
		return acp.LoadSessionResponse{}, acp.ErrInternalError(err.Error(), nil)
	}
	logger.Infof(ctx, "[acp] session loaded: id=%s", st.meta.SessionID)
	return acp.LoadSessionResponse{ConfigOptions: opts}, nil
}

// replayHistory streams the session's on-disk messages as session/update
// notifications. Best-effort throughout: individual load/emit failures are
// logged, never fatal — the client already holds the same history on its
// side, and resume is the hot path.
func (a *Agent) replayHistory(ctx context.Context, st *sessionState) {
	repo, err := store.NewChatContextRepo(st.dir)
	if err != nil {
		logger.Warnf(ctx, "[acp] replay: create repo failed: session=%s err=%v", st.meta.SessionID, err)
		return
	}
	msgs, err := repo.LoadAllMessages(ctx)
	if err != nil {
		logger.Warnf(ctx, "[acp] replay: load messages failed: session=%s err=%v", st.meta.SessionID, err)
		return
	}
	if len(msgs) == 0 {
		return
	}

	tr := newEventTranslator(ctx, a.agentConn, st.meta.SessionID)
	for _, m := range msgs {
		if m == nil || extraBool(m, msgextra.KeyIsSummary) {
			continue
		}
		switch m.Role {
		case schema.User:
			replayUserMessage(tr, m)
		case schema.Assistant:
			replayAssistantMessage(tr, m)
		case schema.Tool:
			replayToolMessage(tr, m)
		default:
			// system / unknown roles have no ACP counterpart.
		}
	}
}

// replayUserMessage re-streams a user turn: multi-content text parts are
// joined, and each image part becomes an `![image](<local_path>)` line so the
// client renders the same attachment affordance it originally sent.
func replayUserMessage(tr *eventTranslator, m *schema.Message) {
	text := m.Content
	if len(m.UserInputMultiContent) > 0 {
		var sb strings.Builder
		for _, part := range m.UserInputMultiContent {
			switch part.Type {
			case schema.ChatMessagePartTypeText:
				sb.WriteString(part.Text)
			case schema.ChatMessagePartTypeImageURL:
				localPath := "image"
				if part.Image != nil {
					if p, ok := part.Image.Extra[msgextra.KeyLocalPath].(string); ok && p != "" {
						localPath = p
					}
				}
				sb.WriteString("![image](" + localPath + ")\n")
			}
		}
		text = sb.String()
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	tr.emitUserText(text)
}

// replayAssistantMessage re-streams one assistant round: reasoning as thought
// chunks, content as message chunks, and each declared tool call as an
// in_progress tool_call plus one args snapshot (the terminal arrives with the
// following role=tool message).
func replayAssistantMessage(tr *eventTranslator, m *schema.Message) {
	if m.ReasoningContent != "" {
		_ = tr.OnThoughtDelta(m.ReasoningContent)
	}
	if m.Content != "" {
		_ = tr.OnMessageDelta(m.Content)
	}
	for _, tc := range m.ToolCalls {
		tr.emitToolCallDeclared(tc.ID, tc.Function.Name)
		if tc.Function.Arguments != "" {
			tr.emitToolArgs(tc.ID, tc.Function.Arguments)
		}
	}
}

// replayToolMessage re-streams a tool result as the terminal update for its
// tool call. Placeholder rows and [failed]-prefixed content replay as failed,
// matching what the live stream showed.
func replayToolMessage(tr *eventTranslator, m *schema.Message) {
	success := !extraBool(m, msgextra.KeyPlaceholderToolResult) && !strings.HasPrefix(m.Content, msgextra.FailedPrefix)
	tr.emitToolTerminal(m.ToolCallID, m.Content, success)
}

// extraBool reads a boolean flag from message Extra (post-unmarshal values
// are plain bools).
func extraBool(m *schema.Message, key string) bool {
	if m.Extra == nil {
		return false
	}
	v, ok := m.Extra[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}
