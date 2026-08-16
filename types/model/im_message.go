package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

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
	Content        string   `json:"content"`
	ToUserIDs      []string `json:"toUserIds"`
	IdempotencyKey string   `json:"idempotencyKey,omitempty"`
}

type WeChatOutboxStatus string

const (
	WeChatOutboxStatusQueued   WeChatOutboxStatus = "queued"
	WeChatOutboxStatusSending  WeChatOutboxStatus = "sending"
	WeChatOutboxStatusRetrying WeChatOutboxStatus = "retrying"
	WeChatOutboxStatusSent     WeChatOutboxStatus = "sent"
)

// WeChatOutboxTask is one durable recipient delivery. Content remains intact;
// the worker derives deterministic UTF-8 chunks and advances NextChunk only
// after the corresponding WeChat call succeeds.
type WeChatOutboxTask struct {
	ID             string             `json:"id"`
	IdempotencyKey string             `json:"idempotencyKey,omitempty"`
	ToUserID       string             `json:"toUserId"`
	Content        string             `json:"content"`
	Status         WeChatOutboxStatus `json:"status"`
	NextChunk      int                `json:"nextChunk"`
	TotalChunks    int                `json:"totalChunks"`
	Attempt        int                `json:"attempt"`
	LastError      string             `json:"lastError,omitempty"`
	NextAttemptAt  *time.Time         `json:"nextAttemptAt,omitempty"`
	LastSentAt     *time.Time         `json:"lastSentAt,omitempty"`
	CreatedAt      time.Time          `json:"createdAt"`
	UpdatedAt      time.Time          `json:"updatedAt"`
	SentAt         *time.Time         `json:"sentAt,omitempty"`
}

// WeChatSendResult reports the durable outbox task created or reused for one
// recipient. Chunks is the number already acknowledged by WeChat.
type WeChatSendResult struct {
	TaskID      string             `json:"taskId"`
	ToUserID    string             `json:"toUserId"`
	Status      WeChatOutboxStatus `json:"status"`
	Chunks      int                `json:"chunks"`
	TotalChunks int                `json:"totalChunks"`
	Error       string             `json:"error,omitempty"`
}

// WeChatSendMessageResponse aggregates the per-recipient results.
type WeChatSendMessageResponse struct {
	Results []WeChatSendResult `json:"results"`
}

type WeChatOutboxTaskResponse struct {
	Task *WeChatOutboxTask `json:"task,omitempty"`
}

type WeChatOutboxResultResponse struct {
	Result *WeChatSendResult `json:"result,omitempty"`
}

func NewWeChatOutboxTaskID() string {
	t := time.Now()
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("wxo-%s-%06d-%s", t.Format("20060102-150405"), t.Nanosecond()/1000, hex.EncodeToString(buf[:]))
}

func DeterministicWeChatOutboxTaskID(idempotencyKey, toUserID string) string {
	sum := sha256.Sum256([]byte("wechat-outbox\x00" + idempotencyKey + "\x00" + toUserID))
	return "wxo-" + hex.EncodeToString(sum[:16])
}
