package tokenizer

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestMessagesTokenCounter(t *testing.T) {
	ctx := context.Background()

	t.Run("nil slice", func(t *testing.T) {
		if got := MessagesTokenCounter(ctx, nil); got != 0 {
			t.Fatalf("expected 0, got %d", got)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		if got := MessagesTokenCounter(ctx, []*schema.Message{}); got != 0 {
			t.Fatalf("expected 0, got %d", got)
		}
	})

	t.Run("sums messages and skips nil", func(t *testing.T) {
		msg1 := schema.UserMessage("hello")
		msg2 := schema.AssistantMessage("world", nil)
		msgs := []*schema.Message{nil, msg1, msg2}

		want := MessageTokenCounter(ctx, msg1) + MessageTokenCounter(ctx, msg2)
		got := MessagesTokenCounter(ctx, msgs)
		if got != want {
			t.Fatalf("expected %d, got %d", want, got)
		}

		if _, ok := getCachedTokenCount(msg1); !ok {
			t.Fatalf("expected msg1 token count cached")
		}
		if _, ok := getCachedTokenCount(msg2); !ok {
			t.Fatalf("expected msg2 token count cached")
		}
	})
}
