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
