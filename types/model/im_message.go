package model

import "time"

type IMMessage struct {
	MessageID   string `json:"messageId"`
	Platform    string `json:"platform"`
	ChatID      string `json:"chatId"`
	ChatType    string `json:"chatType"`
	SenderID    string `json:"senderId"`
	MessageType string `json:"messageType"`
	Content     string `json:"content"`
	ReceivedAt  int64  `json:"receivedAt"`
}

func NewIMMessage(platform, messageID, chatID, chatType, senderID, messageType, content string) *IMMessage {
	return &IMMessage{
		MessageID:   messageID,
		Platform:    platform,
		ChatID:      chatID,
		ChatType:    chatType,
		SenderID:    senderID,
		MessageType: messageType,
		Content:     content,
		ReceivedAt:  time.Now().UnixMilli(),
	}
}

// WeChatSendMessageRequest is the payload of POST /api/v1/wechat/send, the
// proactive (not reply-driven) WeChat push used by scheduled jobs. When
// ToUserIDs is empty the backend fans out to the configured WeChat admin
// whitelist (settings.wechat_admin_ids).
type WeChatSendMessageRequest struct {
	Content   string   `json:"content"`
	ToUserIDs []string `json:"toUserIds"`
}

// WeChatSendResult reports the per-recipient outcome of a WeChatSendMessage
// call. Error is empty on success and carries the full send error otherwise.
type WeChatSendResult struct {
	ToUserID string `json:"toUserId"`
	Chunks   int    `json:"chunks"`
	Error    string `json:"error,omitempty"`
}

// WeChatSendMessageResponse aggregates the per-recipient results.
type WeChatSendMessageResponse struct {
	Results []WeChatSendResult `json:"results"`
}
