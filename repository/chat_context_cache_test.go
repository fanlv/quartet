package repository

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func newTestChatRepo(t *testing.T) ChatContextRepo {
	t.Helper()
	root := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir memory root: %v", err)
	}
	t.Setenv("LOCAL_MEMORY", root)
	repo, err := NewChatContextRepo("ws-test", "job-test", "session-test")
	if err != nil {
		t.Fatalf("NewChatContextRepo: %v", err)
	}
	return repo
}

func userMsg(content string) *schema.Message {
	return &schema.Message{Role: schema.User, Content: content}
}

// TestMessagesCacheColdThenWarmConsistent asserts that a second read (cache
// hit) returns the same content as the first (disk read).
func TestMessagesCacheColdThenWarmConsistent(t *testing.T) {
	repo := newTestChatRepo(t)
	ctx := context.Background()
	if err := repo.AppendMessages(ctx, []*schema.Message{userMsg("a"), userMsg("b")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	cold, err := repo.LoadAllMessages(ctx)
	if err != nil {
		t.Fatalf("cold load: %v", err)
	}
	warm, err := repo.LoadAllMessages(ctx)
	if err != nil {
		t.Fatalf("warm load: %v", err)
	}
	if len(cold) != 2 || len(warm) != 2 {
		t.Fatalf("len cold=%d warm=%d, want 2/2", len(cold), len(warm))
	}
	if warm[0].Content != "a" || warm[1].Content != "b" {
		t.Fatalf("warm content = %q,%q want a,b", warm[0].Content, warm[1].Content)
	}
}

// TestMessagesCacheInvalidatedOnAppend asserts a write invalidates the cache so
// the next read reflects the new content rather than a stale hit.
func TestMessagesCacheInvalidatedOnAppend(t *testing.T) {
	repo := newTestChatRepo(t)
	ctx := context.Background()
	if err := repo.AppendMessages(ctx, []*schema.Message{userMsg("a")}); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if _, err := repo.LoadAllMessages(ctx); err != nil { // warm the cache
		t.Fatalf("warm: %v", err)
	}
	if err := repo.AppendMessages(ctx, []*schema.Message{userMsg("b")}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	got, err := repo.LoadAllMessages(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got) != 2 || got[1].Content != "b" {
		t.Fatalf("after append got %d msgs (last=%q), want 2 ending in b", len(got), lastContent(got))
	}
}

// TestMessagesCacheInvalidatedOnReplace asserts ReplaceMessages busts the cache.
func TestMessagesCacheInvalidatedOnReplace(t *testing.T) {
	repo := newTestChatRepo(t)
	ctx := context.Background()
	if err := repo.AppendMessages(ctx, []*schema.Message{userMsg("a"), userMsg("b")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := repo.LoadAllMessages(ctx); err != nil { // warm
		t.Fatalf("warm: %v", err)
	}
	if err := repo.ReplaceMessages(ctx, []*schema.Message{userMsg("c")}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := repo.LoadAllMessages(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got) != 1 || got[0].Content != "c" {
		t.Fatalf("after replace got %d msgs (first=%q), want 1=c", len(got), firstContent(got))
	}
}

// TestMessagesCacheDisabled asserts the env flag disables caching (reads still
// correct, just never served from cache).
func TestMessagesCacheDisabled(t *testing.T) {
	c := newMessagesCache(0)
	c.put("k", 1, 1, []*schema.Message{userMsg("x")}, 100)
	if _, ok := c.get("k", 1, 1); ok {
		t.Fatal("disabled cache returned a hit")
	}
}

// TestMessagesCacheStaleSignatureMiss asserts a changed (size, mtime) is a miss.
func TestMessagesCacheStaleSignatureMiss(t *testing.T) {
	c := newMessagesCache(1 << 20)
	c.put("k", 10, 100, []*schema.Message{userMsg("x")}, 100)
	if _, ok := c.get("k", 10, 100); !ok {
		t.Fatal("matching signature should hit")
	}
	if _, ok := c.get("k", 11, 100); ok {
		t.Fatal("different size should miss")
	}
	if _, ok := c.get("k", 10, 101); ok {
		t.Fatal("different mtime should miss")
	}
}

// TestMessagesCacheByteBudgetEvicts asserts the LRU evicts to stay within the
// byte budget.
func TestMessagesCacheByteBudgetEvicts(t *testing.T) {
	c := newMessagesCache(250)
	c.put("a", 1, 1, []*schema.Message{userMsg("a")}, 100)
	c.put("b", 1, 1, []*schema.Message{userMsg("b")}, 100)
	// Touch "a" so "b" becomes least-recently-used.
	if _, ok := c.get("a", 1, 1); !ok {
		t.Fatal("a should still be present")
	}
	c.put("c", 1, 1, []*schema.Message{userMsg("c")}, 100) // total would be 300 > 250
	if _, ok := c.get("b", 1, 1); ok {
		t.Fatal("b should have been evicted as LRU")
	}
	if _, ok := c.get("a", 1, 1); !ok {
		t.Fatal("a should survive (recently used)")
	}
	if _, ok := c.get("c", 1, 1); !ok {
		t.Fatal("c should be present (just inserted)")
	}
}

// TestMessagesCacheConcurrentReadWrite exercises the read/write paths under the
// race detector to confirm cache access stays serialised by the per-session
// lock and never tears.
func TestMessagesCacheConcurrentReadWrite(t *testing.T) {
	repo := newTestChatRepo(t)
	ctx := context.Background()
	if err := repo.AppendMessages(ctx, []*schema.Message{userMsg("seed")}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				if _, err := repo.LoadAllMessages(ctx); err != nil {
					t.Errorf("concurrent load: %v", err)
					return
				}
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 20 {
				if err := repo.AppendMessages(ctx, []*schema.Message{userMsg("x")}); err != nil {
					t.Errorf("concurrent append: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func lastContent(msgs []*schema.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Content
}

func firstContent(msgs []*schema.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[0].Content
}
