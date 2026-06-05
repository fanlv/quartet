package model

import "time"

// UserInput source values.
const (
	UserInputSourceIM  = "im"
	UserInputSourceWeb = "web"
)

// UserInput platform values. Mirrors messaging.Platform strings so IM entries
// stay round-trippable; Web entries use UserInputPlatformWeb.
const (
	UserInputPlatformLark   = "lark"
	UserInputPlatformWeChat = "wechat"
	UserInputPlatformWeb    = "web"
)

// UserInput kind values. Entries without an explicit kind (older rows) are
// implicitly "message"; loop_start is the structured "user clicked Start Loop"
// record (see docs/feature-2026-05-07-loop-start-logging.md).
const (
	UserInputKindMessage   = "message"
	UserInputKindLoopStart = "loop_start"
)

// UserInput is the narrow-spec "真实用户输入" record persisted to
// {LOCAL_MEMORY}/user_input/YYYY-MM-DD.jsonl. IM and Web both write into this
// single flat stream; per-source fields are populated when relevant and left
// empty otherwise (no per-source subtypes — keeps reader logic single-shape).
type UserInput struct {
	MessageID  string `json:"messageId"`
	ReceivedAt int64  `json:"receivedAt"`

	Source   string `json:"source"`
	Platform string `json:"platform"`

	ChatID      string `json:"chatId,omitempty"`
	SenderID    string `json:"senderId,omitempty"`
	JobID       string `json:"jobId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`

	Content   string   `json:"content"`
	ImageUrls []string `json:"imageUrls,omitempty"`

	// Kind distinguishes entry shapes in the single flat stream. Empty means
	// "message" for backward compatibility with entries written before
	// loop_start existed.
	Kind string `json:"kind,omitempty"`

	// LoopConfig is the snapshot captured when the user successfully started a
	// Loop. Only populated when Kind == UserInputKindLoopStart.
	LoopConfig *LoopConfig `json:"loopConfig,omitempty"`
}

// NewIMUserInput builds a UserInput for an IM-sourced message. jobID and
// workspaceID are best-effort: empty when the chat has no prior IM->Job
// mapping (the very first message on a new chat), filled from the existing
// mapping otherwise (see docs/feature-2026-05-03-user-input-logging.md §3.5).
//
// receivedAt must be sampled by the caller at the handler's entry point
// (before any synchronous work that could push the timestamp past a
// midnight boundary and land the entry in the wrong daily file).
func NewIMUserInput(receivedAt time.Time, platform, messageID, chatID, senderID, jobID, workspaceID, content string) *UserInput {
	return &UserInput{
		MessageID:   messageID,
		ReceivedAt:  receivedAt.UnixMilli(),
		Source:      UserInputSourceIM,
		Platform:    platform,
		ChatID:      chatID,
		SenderID:    senderID,
		JobID:       jobID,
		WorkspaceID: workspaceID,
		Content:     content,
	}
}

// NewWebUserInput builds a UserInput for a Web-sourced message.
//
// receivedAt must be sampled by the caller at the handler's entry point
// (before prepareJobSend's potentially slow image reads / base64 work),
// so the timestamp reflects "when the server received" the request and
// stays on the correct side of a midnight boundary.
func NewWebUserInput(receivedAt time.Time, messageID, jobID, workspaceID, content string, imageUrls []string) *UserInput {
	return &UserInput{
		MessageID:   messageID,
		ReceivedAt:  receivedAt.UnixMilli(),
		Source:      UserInputSourceWeb,
		Platform:    UserInputPlatformWeb,
		JobID:       jobID,
		WorkspaceID: workspaceID,
		Content:     content,
		ImageUrls:   imageUrls,
	}
}

// NewLoopStartUserInput builds a UserInput that records a successful "Start
// Loop" action from Web. The full LoopConfig snapshot is embedded verbatim so
// offline readers can replay the exact plan the user committed to
// (docs/feature-2026-05-07-loop-start-logging.md §3.1/§3.4). content is a
// human-readable summary; the structured truth lives in LoopConfig.
//
// receivedAt must be sampled at the handler entry point before any work that
// could cross a midnight boundary.
func NewLoopStartUserInput(receivedAt time.Time, messageID, jobID, workspaceID, content string, loopConfig *LoopConfig) *UserInput {
	return &UserInput{
		MessageID:   messageID,
		ReceivedAt:  receivedAt.UnixMilli(),
		Source:      UserInputSourceWeb,
		Platform:    UserInputPlatformWeb,
		JobID:       jobID,
		WorkspaceID: workspaceID,
		Content:     content,
		Kind:        UserInputKindLoopStart,
		LoopConfig:  loopConfig,
	}
}
