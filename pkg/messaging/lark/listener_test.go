package lark

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fanlv/quartet/pkg/messaging"
)

type captureHandler struct {
	msg *messaging.Message
}

func (h *captureHandler) OnMessage(_ context.Context, msg *messaging.Message) {
	h.msg = msg
}

func TestHandleIMMessageMapsMentions(t *testing.T) {
	handler := &captureHandler{}
	listener := &Listener{handler: handler}
	raw := &rawEvent{
		Event: []byte(`{
			"message": {
				"message_id": "om_x100b5178098aeca4b280cec737e0568",
				"parent_id": "om_x100b517979b120a0b4afbe6ae3d697c",
				"root_id": "om_x100b517979b120a0b4afbe6ae3d697c",
				"chat_id": "oc_dec73728432aac4b38923ee178aa39de",
				"chat_type": "group",
				"message_type": "text",
				"content": "{\"text\":\"@_user_1 1212\"}",
				"create_time": "1776605048194",
				"update_time": "1776605049194",
				"mentions": [
					{
						"id": {
							"open_id": "ou_27ade7346f1dc5a485d4de0c5944dd13",
							"union_id": "on_4edd5a3c8da34771083511052b9cddbe",
							"user_id": null
						},
						"key": "@_user_1",
						"mentioned_type": "bot",
						"name": "Sophia",
						"tenant_key": "736588c9260f175d"
					}
				]
			},
			"sender": {
				"sender_id": {
					"open_id": "ou_83e067208fac1cc307e9ce95257f79dd"
				},
				"sender_type": "user"
			}
		}`),
	}

	listener.handleIMMessage(context.Background(), raw)

	if handler.msg == nil {
		t.Fatal("expected message to be delivered to handler")
	}
	if handler.msg.Content != "@Sophia 1212" {
		t.Fatalf("expected text content to be decoded, got %q", handler.msg.Content)
	}
	if handler.msg.ParentID != "om_x100b517979b120a0b4afbe6ae3d697c" {
		t.Fatalf("expected parent_id to be preserved, got %q", handler.msg.ParentID)
	}
	if handler.msg.RootID != "om_x100b517979b120a0b4afbe6ae3d697c" {
		t.Fatalf("expected root_id to be preserved, got %q", handler.msg.RootID)
	}
	if handler.msg.CreateTime != "1776605048194" {
		t.Fatalf("expected create_time to be preserved, got %q", handler.msg.CreateTime)
	}
	if handler.msg.UpdateTime != "1776605049194" {
		t.Fatalf("expected update_time to be preserved, got %q", handler.msg.UpdateTime)
	}
	if len(handler.msg.Mentions) != 1 {
		t.Fatalf("expected 1 mention, got %d", len(handler.msg.Mentions))
	}

	mention := handler.msg.Mentions[0]
	if mention.Key != "@_user_1" {
		t.Fatalf("expected mention key to be preserved, got %q", mention.Key)
	}
	if mention.Name != "Sophia" {
		t.Fatalf("expected mention name to be preserved, got %q", mention.Name)
	}
	if mention.MentionedType != "bot" {
		t.Fatalf("expected mention type to be preserved, got %q", mention.MentionedType)
	}
	if mention.ID.OpenID != "ou_27ade7346f1dc5a485d4de0c5944dd13" {
		t.Fatalf("expected mention open_id to be preserved, got %q", mention.ID.OpenID)
	}
	if mention.ID.UserID != "" {
		t.Fatalf("expected null user_id to map to empty string, got %q", mention.ID.UserID)
	}
}

func TestHandleIMMessageDecodesPostContent(t *testing.T) {
	handler := &captureHandler{}
	listener := &Listener{
		handler: handler,
		imageDownloader: func(_ context.Context, messageID, imageKey string) (string, error) {
			if messageID != "om_x100b517979b120a0b4afbe6ae3d697c" {
				t.Fatalf("unexpected messageID: %s", messageID)
			}
			if imageKey != "img_v3_0210t_demo" {
				t.Fatalf("unexpected imageKey: %s", imageKey)
			}
			return "/tmp/quartet-lark/image-demo.png", nil
		},
	}
	raw := &rawEvent{
		Event: []byte(`{
			"message": {
				"message_id": "om_x100b517979b120a0b4afbe6ae3d697c",
				"chat_id": "oc_dec73728432aac4b38923ee178aa39de",
				"chat_type": "group",
				"message_type": "post",
				"content": "{\"title\":\"\",\"content\":[[{\"tag\":\"at\",\"user_id\":\"@_user_1\",\"user_name\":\"Sophia\"},{\"tag\":\"text\",\"text\":\" 你能看到这个图片吗？\"}],[],[{\"tag\":\"img\",\"image_key\":\"img_v3_0210t_demo\"}]]}",
				"mentions": [
					{
						"id": {
							"open_id": "ou_27ade7346f1dc5a485d4de0c5944dd13",
							"union_id": "on_4edd5a3c8da34771083511052b9cddbe",
							"user_id": null
						},
						"key": "@_user_1",
						"mentioned_type": "bot",
						"name": "Sophia",
						"tenant_key": "736588c9260f175d"
					}
				]
			},
			"sender": {
				"sender_id": {
					"open_id": "ou_83e067208fac1cc307e9ce95257f79dd"
				},
				"sender_type": "user"
			}
		}`),
	}

	listener.handleIMMessage(context.Background(), raw)

	if handler.msg == nil {
		t.Fatal("expected message to be delivered to handler")
	}

	want := "@Sophia 你能看到这个图片吗？\n\n![image](/tmp/quartet-lark/image-demo.png)"
	if handler.msg.Content != want {
		t.Fatalf("expected post content to be decoded, got %q", handler.msg.Content)
	}
}

func TestHandleIMMessageDecodesImageContent(t *testing.T) {
	handler := &captureHandler{}
	listener := &Listener{
		handler: handler,
		imageDownloader: func(_ context.Context, messageID, imageKey string) (string, error) {
			if messageID != "om_x100b51793d921c74b2cbf47a411194c" {
				t.Fatalf("unexpected messageID: %s", messageID)
			}
			if imageKey != "img_v3_0210t_d0624d32-d90e-4190-9954-f54c11b4f10g" {
				t.Fatalf("unexpected imageKey: %s", imageKey)
			}
			return "/tmp/quartet-lark/image-single.png", nil
		},
	}
	raw := &rawEvent{
		Event: []byte(`{
			"message": {
				"message_id": "om_x100b51793d921c74b2cbf47a411194c",
				"chat_id": "oc_dec73728432aac4b38923ee178aa39de",
				"chat_type": "group",
				"message_type": "image",
				"content": "{\"image_key\":\"img_v3_0210t_d0624d32-d90e-4190-9954-f54c11b4f10g\"}"
			},
			"sender": {
				"sender_id": {
					"open_id": "ou_83e067208fac1cc307e9ce95257f79dd"
				},
				"sender_type": "user"
			}
		}`),
	}

	listener.handleIMMessage(context.Background(), raw)

	if handler.msg == nil {
		t.Fatal("expected message to be delivered to handler")
	}

	want := "![image](/tmp/quartet-lark/image-single.png)"
	if handler.msg.Content != want {
		t.Fatalf("expected image content to be decoded, got %q", handler.msg.Content)
	}
}

// TestHandleIMMessageDownloadsPostImagesInParallel verifies the fix for
// serial multi-image downloads: when a post references N distinct images,
// they must be fetched concurrently rather than N × single-image latency.
func TestHandleIMMessageDownloadsPostImagesInParallel(t *testing.T) {
	const (
		perImageDelay = 80 * time.Millisecond
		imageCount    = 4
	)

	var inflight atomic.Int32
	var maxInflight atomic.Int32

	handler := &captureHandler{}
	listener := &Listener{
		handler: handler,
		imageDownloader: func(_ context.Context, messageID, imageKey string) (string, error) {
			cur := inflight.Add(1)
			for {
				m := maxInflight.Load()
				if cur <= m || maxInflight.CompareAndSwap(m, cur) {
					break
				}
			}
			time.Sleep(perImageDelay)
			inflight.Add(-1)
			return "/tmp/" + imageKey + ".png", nil
		},
	}
	raw := &rawEvent{
		Event: []byte(`{
			"message": {
				"message_id": "om_parallel_test",
				"chat_id": "oc_dec73728432aac4b38923ee178aa39de",
				"chat_type": "group",
				"message_type": "post",
				"content": "{\"title\":\"\",\"content\":[[{\"tag\":\"img\",\"image_key\":\"k1\"}],[{\"tag\":\"img\",\"image_key\":\"k2\"}],[{\"tag\":\"img\",\"image_key\":\"k3\"}],[{\"tag\":\"img\",\"image_key\":\"k4\"}]]}"
			},
			"sender": {"sender_id": {"open_id": "ou_x"}, "sender_type": "user"}
		}`),
	}

	start := time.Now()
	listener.handleIMMessage(context.Background(), raw)
	elapsed := time.Since(start)

	if got := maxInflight.Load(); got < 2 {
		t.Fatalf("expected >=2 concurrent downloads, got max=%d (serial regression)", got)
	}
	// Serial would be imageCount*perImageDelay = 320ms; parallel should finish
	// close to perImageDelay. Allow 2× headroom for slow CI.
	if ceiling := time.Duration(imageCount-1) * perImageDelay; elapsed >= ceiling {
		t.Fatalf("expected parallel download to finish < %v, took %v", ceiling, elapsed)
	}
	if handler.msg == nil {
		t.Fatal("expected message to be delivered to handler")
	}
}

// TestUniquePostImageKeysDeduplicates ensures duplicate imageKeys trigger
// only a single download, which matters for posts that reference the same
// image in multiple places.
func TestUniquePostImageKeysDeduplicates(t *testing.T) {
	post := &rawPostContent{
		Content: [][]rawPostElement{
			{{Tag: "img", ImageKey: "a"}, {Tag: "img", ImageKey: "b"}},
			{{Tag: "img", ImageKey: "a"}, {Tag: "text", Text: "x"}},
			{{Tag: "img", ImageKey: ""}},
		},
	}
	keys := uniquePostImageKeys(post)
	if len(keys) != 2 {
		t.Fatalf("expected 2 unique keys, got %d: %v", len(keys), keys)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("duplicate key %q in result", k)
		}
		seen[k] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("expected keys a,b, got %v", keys)
	}
}

// TestDownloadPostImagesRecordsFailures verifies that a failed download
// shows up as an empty string in the result map (so callers emit the
// fallback placeholder) rather than crashing or silently dropping the key.
func TestDownloadPostImagesRecordsFailures(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	listener := &Listener{
		imageDownloader: func(_ context.Context, _, imageKey string) (string, error) {
			mu.Lock()
			calls[imageKey]++
			mu.Unlock()
			if imageKey == "bad" {
				return "", context.DeadlineExceeded
			}
			return "/tmp/" + imageKey + ".png", nil
		},
	}
	post := &rawPostContent{
		Content: [][]rawPostElement{
			{{Tag: "img", ImageKey: "good"}, {Tag: "img", ImageKey: "bad"}},
		},
	}
	got := listener.downloadPostImages(context.Background(), "m1", post)
	if got["good"] != "/tmp/good.png" {
		t.Fatalf("expected good path, got %q", got["good"])
	}
	if got["bad"] != "" {
		t.Fatalf("expected empty path for failed download, got %q", got["bad"])
	}
}

func TestIsUnsupportedEventLog(t *testing.T) {
	if !isUnsupportedEventLog("handle message failed, message_type: event, message_id: fc60a9be-234a-4289-be8e-93f047bb104b, trace_id: 5a00dc5a72dc5b66319ff534da365861, err: event type: im.message.reaction.created_v1, not found handler[conn_id=7636689797957290970]") {
		t.Fatal("expected unsupported event log to be downgraded")
	}
	if isUnsupportedEventLog("handle message failed, message_type: event, err: read tcp 127.0.0.1:1->127.0.0.1:2: i/o timeout") {
		t.Fatal("expected transport errors to remain error level")
	}
}

func TestIsRecoverableWSDisconnectLog(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{
			name: "close 1006 abnormal closure with unexpected EOF",
			msg:  "receive message failed, err: websocket: close 1006 (abnormal closure): unexpected EOF[conn_id=7638869598264577238]",
			want: true,
		},
		{
			name: "close 1001 going away",
			msg:  "receive message failed, err: websocket: close 1001 (going away)",
			want: true,
		},
		{
			name: "use of closed network connection during shutdown",
			msg:  "receive message failed, err: read tcp: use of closed network connection",
			want: true,
		},
		{
			name: "connection reset by peer on websocket",
			msg:  "websocket: connection reset by peer",
			want: true,
		},
		{
			name: "non-recoverable parse error keeps ERROR",
			msg:  "handle message failed, err: invalid frame opcode: 99",
			want: false,
		},
		{
			name: "auth failure keeps ERROR",
			msg:  "auth failed: invalid app secret",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRecoverableWSDisconnectLog(c.msg); got != c.want {
				t.Fatalf("isRecoverableWSDisconnectLog(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}
