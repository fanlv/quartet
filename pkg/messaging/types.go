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

//	{
//	    "schema": "2.0",
//	    "header": {
//	        "event_id": "72e3e3b7a5f22c98927e972fe31a0af5",
//	        "token": "",
//	        "create_time": "1776604024209",
//	        "event_type": "im.message.receive_v1",
//	        "tenant_key": "736588c9260f175d",
//	        "app_id": "cli_a942c75537e19bdd"
//	    },
//	    "event": {
//	        "message": {
//	            "chat_id": "oc_dec73728432aac4b38923ee178aa39de",
//	            "chat_type": "group",
//	            "content": "{\"title\":\"\",\"content\":[[{\"tag\":\"at\",\"user_id\":\"@_user_1\",\"user_name\":\"Sophia\",\"style\":[]},{\"tag\":\"text\",\"text\":\" 你能看到这个图片吗？\",\"style\":[]}],[],[{\"tag\":\"img\",\"image_key\":\"img_v3_0210t_d51cd372-a090-43ab-823e-29c7098b6dbg\",\"width\":384,\"height\":192}]]}",
//	            "create_time": "1776604023910",
//	            "mentions": [
//	                {
//	                    "id": {
//	                        "open_id": "ou_27ade7346f1dc5a485d4de0c5944dd13",
//	                        "union_id": "on_4edd5a3c8da34771083511052b9cddbe",
//	                        "user_id": null
//	                    },
//	                    "key": "@_user_1",
//	                    "mentioned_type": "bot",
//	                    "name": "Sophia",
//	                    "tenant_key": "736588c9260f175d"
//	                }
//	            ],
//	            "message_id": "om_x100b517979b120a0b4afbe6ae3d697c",
//	            "message_type": "post",
//	            "update_time": "1776604023910"
//	        },
//	        "sender": {
//	            "sender_id": {
//	                "open_id": "ou_83e067208fac1cc307e9ce95257f79dd",
//	                "union_id": "on_e777c0f6af1feeb2a9576a6ff84db2bc",
//	                "user_id": null
//	            },
//	            "sender_type": "user",
//	            "tenant_key": "736588c9260f175d"
//	        }
//	    }
//	}
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
