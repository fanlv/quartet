package handler

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/tokenizer"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/msgextra"
)

func (h *Handler) GetSessionMessages(ctx context.Context, c *app.RequestContext) {
	sessionID := c.Param("sessionId")
	if sessionID == "" {
		httputil.BadRequest(c, "sessionId is required")
		return
	}

	s, _, ok := h.getSessionByID(sessionID)
	if !ok {
		// Session service may have been evicted from memory (idle timeout or
		// loop-job completion). Try to find the owning job and reload from disk.
		s, ok = h.reloadSessionByID(sessionID)
		if !ok {
			httputil.NotFound(c, "session not found")
			return
		}
	}

	j, jok := h.jobService.Get(s.JobID)
	if !jok {
		httputil.NotFound(c, "job not found for session")
		return
	}

	ctxMgr, err := repository.NewChatContextRepo(j.WorkspaceID, s.JobID, sessionID)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	// messages.jsonl is a mirror rebuilt from ACP events (same as claude
	// etc.), so history is projected verbatim — compression, if any,
	// happens inside the agent subprocess and never rewrites the mirror.
	chatMessages, err := ctxMgr.LoadAllMessages(ctx)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	messages := make([]model.HistoryMessage, 0, len(chatMessages))

	for i, msg := range chatMessages {
		if msg.Role == schema.System {
			continue
		}

		content := msg.Content
		var imageUrls []string

		// Extract text content and image URLs from UserInputMultiContent (multimodal user input)
		if len(msg.UserInputMultiContent) > 0 {
			var textParts []string
			for _, part := range msg.UserInputMultiContent {
				switch part.Type {
				case schema.ChatMessagePartTypeText:
					if part.Text != "" {
						textParts = append(textParts, part.Text)
					}
				case schema.ChatMessagePartTypeImageURL:
					if part.Image != nil {
						if lp, ok := part.Image.Extra[msgextra.KeyLocalPath].(string); ok && lp != "" {
							imageUrls = append(imageUrls, lp)
						} else if part.Image.URL != nil && *part.Image.URL != "" {
							imageUrls = append(imageUrls, *part.Image.URL)
						} else if part.Image.Base64Data != nil && *part.Image.Base64Data != "" {
							mimeType := part.Image.MIMEType
							if mimeType == "" {
								mimeType = "image/png"
							}
							imageUrls = append(imageUrls, fmt.Sprintf("data:%s;base64,%s", mimeType, *part.Image.Base64Data))
						}
					}
				}
			}
			if len(textParts) > 0 {
				content = strings.Join(textParts, "\n")
			}
		} else if msg.Role == schema.User {
			content, imageUrls = extractHistoryLocalImageURLs(content)
		}

		historyMsg := model.HistoryMessage{
			ID:               fmt.Sprintf("%s:msg_%d", sessionID, i),
			Role:             model.MessageRole(msg.Role),
			Content:          content,
			ReasoningContent: msg.ReasoningContent,
			ImageUrls:        imageUrls,
		}

		// Prefer the stable SSE message ID stored during persistence so that
		// the frontend can correlate history entries with live SSE events.
		if msg.Extra != nil {
			if mid, ok := msg.Extra[msgextra.KeyMsgID].(string); ok && mid != "" {
				historyMsg.ID = mid
			}
		}

		// Duration timestamps from Extra
		if msg.Extra != nil {
			if v, ok := msg.Extra[msgextra.KeyStartedAt]; ok {
				historyMsg.StartedAt = toInt64(v)
			}
			if v, ok := msg.Extra[msgextra.KeyFinishedAt]; ok {
				historyMsg.FinishedAt = toInt64(v)
			}
			if v, ok := msg.Extra[msgextra.KeyThoughtStartedAt]; ok {
				historyMsg.ThoughtStartedAt = toInt64(v)
			}
			if v, ok := msg.Extra[msgextra.KeyThoughtFinishedAt]; ok {
				historyMsg.ThoughtFinishedAt = toInt64(v)
			}
		}

		// If the message has a separate thought SSE ID, emit the thought as
		// its own history entry so the frontend can correlate both SSE
		// messages (thought + content) with their history counterparts.
		// The thought entry carries the thinking time window so the UI can
		// render the duration badge on history replay.
		if msg.Extra != nil {
			if tid, ok := msg.Extra[msgextra.KeyThoughtMsgID].(string); ok && tid != "" && msg.ReasoningContent != "" {
				thoughtMsg := model.HistoryMessage{
					ID:                tid,
					Role:              model.MessageRole(msg.Role),
					ReasoningContent:  msg.ReasoningContent,
					IsThinking:        true,
					StartedAt:         historyMsg.ThoughtStartedAt,
					ThoughtStartedAt:  historyMsg.ThoughtStartedAt,
					ThoughtFinishedAt: historyMsg.ThoughtFinishedAt,
				}
				messages = append(messages, thoughtMsg)
				// Remove reasoning from the main entry since it's now separate.
				historyMsg.ReasoningContent = ""
			}
		}

		if msg.Extra != nil {
			if v, ok := msg.Extra[msgextra.KeyShellOutput]; ok {
				if b, ok := v.(bool); ok && b {
					historyMsg.IsShellOutput = true
				}
			}
		}

		if msg.ToolCallID != "" {
			historyMsg.ToolCallID = msg.ToolCallID
			// The round builder prefixes failed tool results with
			// msgextra.FailedPrefix on disk so the LLM sees the failure in
			// subsequent rounds. Surface that as a structured flag here
			// so the frontend can render the error badge without parsing
			// the content itself. Placeholder messages (distinct prefix
			// "[placeholder]") are NOT failed; they get their own
			// Placeholder flag below.
			if strings.HasPrefix(content, msgextra.FailedPrefix) {
				historyMsg.Failed = true
			}

			// Placeholder tool results were synthesised by the round
			// builder when a round flushed without a real tool result —
			// typically because the run was cancelled / interrupted, or
			// because a later message superseded an incomplete round.
			// The client would otherwise paint these as green
			// "Completed" bubbles on reload (content doesn't match
			// "[failed]", and the post-load sweep forces every
			// Processing tool to Success), contradicting what the user
			// last saw before the interruption. Surface the placeholder
			// flag + reason so the UI can render them with a distinct
			// "incomplete" state.
			if msg.Extra != nil {
				if v, ok := msg.Extra[msgextra.KeyPlaceholderToolResult]; ok {
					if b, ok := v.(bool); ok && b {
						historyMsg.Placeholder = true
						historyMsg.PlaceholderReason = strings.TrimSpace(
							strings.TrimPrefix(content, msgextra.PlaceholderPrefix),
						)
					}
				}
			}
		}

		if len(msg.ToolCalls) > 0 {
			historyMsg.ToolCalls = make([]model.ToolCallInfo, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				historyMsg.ToolCalls[j] = model.ToolCallInfo{
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				}
			}
		}

		messages = append(messages, historyMsg)
	}

	tokens := tokenizer.MessagesTokenCounter(ctx, chatMessages)

	resp := model.GetMessagesResponse{
		ModelID:         s.ModelID,
		Type:            s.Type,
		Messages:        messages,
		TokenUsage:      &model.TokenUsage{TotalTokens: tokens},
		Workdir:         s.Workdir,
		ACPMode:         s.ACPMode,
		ACPThoughtLevel: s.ACPThoughtLevel,
	}
	// Public share responses carry the minimal display projection of the
	// Agent this session references so the read-only share page can render
	// renamed / deleted Agents without any catalog access of its own.
	if _, isPublic := getPublicJob(c); isPublic {
		resp.Agents = h.resolvePublicAgents(ctx, []string{s.Type})
	}
	c.JSON(http.StatusOK, resp)
}

// extractHistoryLocalImageURLs reconstructs imageUrls from the leading markdown
// image lines Quartet prepends to persisted user messages. Parsing whole prefix
// lines (instead of stopping at the first ')') preserves valid POSIX paths that
// contain parentheses and avoids stripping user-authored inline markdown later
// in the message. Remote / placeholder / relative tags remain visible text.
func extractHistoryLocalImageURLs(content string) (string, []string) {
	if content == "" {
		return "", nil
	}

	lines := strings.Split(content, "\n")
	cleanedLines := make([]string, 0, len(lines))
	imageUrls := make([]string, 0)
	readingImagePrefix := true
	const prefix = "![image]("
	for _, line := range lines {
		if readingImagePrefix && strings.HasPrefix(line, prefix) && strings.HasSuffix(line, ")") {
			path := line[len(prefix) : len(line)-1]
			if filepath.IsAbs(path) {
				imageUrls = append(imageUrls, path)
				continue
			}
		}

		readingImagePrefix = false
		cleanedLines = append(cleanedLines, line)
	}

	return strings.Join(cleanedLines, "\n"), imageUrls
}

// toInt64 extracts an int64 from a value that may be stored as float64
// (JSON round-trip via map[string]any) or as int64 (in-memory).
func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	default:
		return 0
	}
}
