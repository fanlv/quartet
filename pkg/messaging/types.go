package messaging

import (
	"context"
	"time"
)

type Platform string

const (
	PlatformLark   Platform = "lark"
	PlatformWeChat Platform = "wechat"
)

type ChatType string

const (
	ChatTypeP2P   ChatType = "p2p"
	ChatTypeGroup ChatType = "group"
)

type MentionID struct {
	OpenID  string
	UnionID string
	UserID  string
}

type Mention struct {
	ID            MentionID
	Key           string
	MentionedType string
	Name          string
	TenantKey     string
}
type Message struct {
	Platform    Platform
	MessageID   string
	ParentID    string
	RootID      string
	ChatID      string
	ChatType    ChatType
	SenderID    string
	MessageType string
	Content     string
	// CreateTime / UpdateTime keep the raw platform timestamp strings for
	// prompt-template compatibility. ReceivedAt is when Quartet accepted the
	// message; EventTime is the parsed platform event time when available.
	CreateTime string
	UpdateTime string
	ReceivedAt time.Time
	EventTime  time.Time
	Mentions   []Mention
	RawEvent   []byte
}

type EventHandler interface {
	OnMessage(ctx context.Context, msg *Message)
}

type Replier interface {
	SendText(ctx context.Context, chatID, content string) error
	ReplyText(ctx context.Context, messageID, content string) error
}

// MediaPayload describes one outbound media attachment. Exactly one of URL or
// Path should be set by callers. URL is used for externally hosted media;
// Path is used for already downloaded local files.
type MediaPayload struct {
	URL  string
	Path string
}

// MediaReplier is an optional extension implemented by platforms that can send
// native media messages. Callers should type-assert this interface and fall
// back to ReplyText when a platform does not support it.
type MediaReplier interface {
	Replier
	ReplyMedia(ctx context.Context, messageID string, media MediaPayload) error
}

type Listener interface {
	Start(ctx context.Context) error
}
