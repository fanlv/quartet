package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
)

type rawMentionID struct {
	OpenID  string  `json:"open_id"`
	UnionID string  `json:"union_id"`
	UserID  *string `json:"user_id"`
}

type rawMention struct {
	ID            rawMentionID `json:"id"`
	Key           string       `json:"key"`
	MentionedType string       `json:"mentioned_type"`
	Name          string       `json:"name"`
	TenantKey     string       `json:"tenant_key"`
}

type rawPostContent struct {
	Title   string             `json:"title"`
	Content [][]rawPostElement `json:"content"`
}

type rawImageContent struct {
	ImageKey string `json:"image_key"`
}

type rawPostElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	UserName string `json:"user_name,omitempty"`
	ImageKey string `json:"image_key,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

func (l *Listener) decodeMessageContent(ctx context.Context, messageID, messageType, content string, mentions []rawMention) string {
	switch messageType {
	case "text":
		var textContent struct {
			Text string `json:"text"`
		}
		err := json.Unmarshal([]byte(content), &textContent)
		if err == nil {
			return replaceMentionKeys(textContent.Text, mentions)
		}
		logger.Warn("[lark] decode text content failed: msg=%s err=%v", messageID, err)
	case "post":
		decoded, err := l.decodePostContent(ctx, messageID, content, mentions)
		if err == nil {
			return decoded
		}
		logger.Warn("[lark] decode post content failed: msg=%s err=%v", messageID, err)
	case "image":
		decoded, err := l.decodeImageContent(ctx, messageID, content)
		if err == nil {
			return decoded
		}
		logger.Warn("[lark] decode image content failed: msg=%s err=%v", messageID, err)
	default:
		// Unsupported types (file/audio/video/sticker/interactive/merge_forward…)
		// fall through to a human-readable placeholder so the Agent doesn't
		// receive raw JSON like {"file_key":"xxx"} as the user's message.
		// This is an expected fallback, not a link-level error — debug-level
		// on purpose so it doesn't dilute real failure logs.
		logger.Debug("[lark] non-text message rendered as placeholder: msg=%s type=%s", messageID, messageType)
		return fmt.Sprintf("暂不支持的消息类型：`%s`", messageType)
	}

	return content
}

func (l *Listener) decodeImageContent(ctx context.Context, messageID, content string) (string, error) {
	var imageContent rawImageContent
	if err := json.Unmarshal([]byte(content), &imageContent); err != nil {
		return "", err
	}

	return l.renderImageReference(ctx, messageID, imageContent.ImageKey), nil
}

func (l *Listener) decodePostContent(ctx context.Context, messageID, content string, mentions []rawMention) (string, error) {
	var post rawPostContent
	if err := json.Unmarshal([]byte(content), &post); err != nil {
		return "", err
	}

	mentionNames := buildMentionNameMap(mentions)
	imagePaths := l.downloadPostImages(ctx, messageID, &post)

	lines := make([]string, 0, len(post.Content)+1)
	if post.Title != "" {
		lines = append(lines, post.Title)
	}

	for _, paragraph := range post.Content {
		if len(paragraph) == 0 {
			lines = append(lines, "")
			continue
		}

		var sb strings.Builder
		for _, elem := range paragraph {
			switch elem.Tag {
			case "text":
				sb.WriteString(elem.Text)
			case "at":
				sb.WriteString(formatMentionDisplayName(mentionNames[elem.UserID], elem.UserName, elem.UserID))
			case "img":
				sb.WriteString(renderImageLocalPath(elem.ImageKey, imagePaths[elem.ImageKey]))
			default:
				if elem.Text != "" {
					sb.WriteString(elem.Text)
				}
			}
		}
		lines = append(lines, sb.String())
	}

	return strings.Join(lines, "\n"), nil
}

func replaceMentionKeys(text string, mentions []rawMention) string {
	if text == "" || len(mentions) == 0 {
		return text
	}

	for _, mention := range mentions {
		if mention.Key == "" {
			continue
		}
		text = strings.ReplaceAll(text, mention.Key, formatMentionDisplayName(mention.Name, mention.Key))
	}

	return text
}

func buildMentionNameMap(mentions []rawMention) map[string]string {
	mentionNames := make(map[string]string, len(mentions))
	for _, mention := range mentions {
		if mention.Key != "" && mention.Name != "" {
			mentionNames[mention.Key] = mention.Name
		}
	}
	return mentionNames
}

func formatMentionDisplayName(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, "@") {
			return candidate
		}
		return "@" + candidate
	}
	return "@unknown"
}

func toMentions(rawMentions []rawMention) []messaging.Mention {
	if len(rawMentions) == 0 {
		return nil
	}

	mentions := make([]messaging.Mention, 0, len(rawMentions))
	for _, rawMention := range rawMentions {
		mention := messaging.Mention{
			ID: messaging.MentionID{
				OpenID:  rawMention.ID.OpenID,
				UnionID: rawMention.ID.UnionID,
			},
			Key:           rawMention.Key,
			MentionedType: rawMention.MentionedType,
			Name:          rawMention.Name,
			TenantKey:     rawMention.TenantKey,
		}
		if rawMention.ID.UserID != nil {
			mention.ID.UserID = *rawMention.ID.UserID
		}
		mentions = append(mentions, mention)
	}

	return mentions
}
