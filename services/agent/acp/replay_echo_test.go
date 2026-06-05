package acp

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/msgextra"
)

func assistantMsg(content string, started, finished int64) *schema.Message {
	m := &schema.Message{Role: schema.Assistant, Content: content}
	if started != 0 || finished != 0 {
		m.Extra = map[string]any{
			msgextra.KeyStartedAt:  started,
			msgextra.KeyFinishedAt: finished,
		}
	}
	return m
}

func TestIsReplayEchoMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  *schema.Message
		want bool
	}{
		// Real streamed replies (taken from observed history): no prefix,
		// hundreds of ms to seconds of elapsed time. Must NOT be dropped.
		{"real reply 襄阳", assistantMsg("襄阳今明两天天气如下：", 1780114355632, 1780114360433), false},
		{"real reply 1+1=2", assistantMsg("1+1=2", 1780455874483, 1780455875457), false},
		{"real reply 1+2=3", assistantMsg("1+2=3", 1780455947150, 1780455948117), false},

		// Replay echoes (observed): prefix + instantaneous. Must be dropped.
		{"echo multi-prefix 襄阳", assistantMsg("[No content][No content][No content]襄阳今明两天天气如下：", 1780455866257, 1780455866257), true},
		{"echo near-instant (1ms)", assistantMsg("[No content][No content][No content]襄阳今明两天天气如下：", 1780455942156, 1780455942157), true},
		{"echo single-prefix 1+1=2", assistantMsg("[No content]1+1=2", 1780455942161, 1780455942161), true},

		// Prefix present but slow → not an echo (defensive: a real reply that
		// somehow leads with the literal text still took real time to stream).
		{"prefix but slow", assistantMsg("[No content]something", 1000, 2000), false},

		// Prefix present, timing missing → prefix alone is decisive.
		{"prefix no timing", assistantMsg("[No content]x", 0, 0), true},

		// Non-assistant roles are never echoes.
		{"user with prefix", &schema.Message{Role: schema.User, Content: "[No content]hi"}, false},
		{"nil message", nil, false},
		{"empty content", assistantMsg("", 1, 1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReplayEchoMessage(tt.msg); got != tt.want {
				t.Errorf("isReplayEchoMessage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDropReplayEchoMessages(t *testing.T) {
	ctx := context.Background()

	t.Run("drops echo keeps real", func(t *testing.T) {
		echo := assistantMsg("[No content]1+1=2", 100, 100)
		real := assistantMsg("1+2=3", 1000, 2000)
		out := dropReplayEchoMessages(ctx, "sess", []*schema.Message{echo, real})
		if len(out) != 1 || out[0] != real {
			t.Fatalf("expected only the real message kept, got %d messages", len(out))
		}
	})

	t.Run("no echo returns input unchanged", func(t *testing.T) {
		real := assistantMsg("hello", 1000, 2000)
		tool := &schema.Message{Role: schema.Tool, Content: "result"}
		in := []*schema.Message{real, tool}
		out := dropReplayEchoMessages(ctx, "sess", in)
		if len(out) != 2 {
			t.Fatalf("expected input returned unchanged, got %d messages", len(out))
		}
	})

	t.Run("all echo drains to empty", func(t *testing.T) {
		echo := assistantMsg("[No content]x", 100, 100)
		out := dropReplayEchoMessages(ctx, "sess", []*schema.Message{echo})
		if len(out) != 0 {
			t.Fatalf("expected empty result, got %d messages", len(out))
		}
	})
}
