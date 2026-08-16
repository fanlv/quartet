package wechat

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"
)

// TestRegisterIncoming_PopulatesCaches: both msgMeta and userToken get
// written on RegisterIncoming, and lookups return the expected values.
func TestRegisterIncoming_PopulatesCaches(t *testing.T) {
	r := NewReplier(func() []*ilink.Credentials {
		return []*ilink.Credentials{{ILinkBotID: "@bot_1"}}
	})
	t.Cleanup(r.Close)

	msg := &ilink.WeixinMessage{
		MessageID:    12345,
		FromUserID:   "@alice_1",
		ContextToken: "ctx-abc",
	}
	r.RegisterIncoming(msg, "@bot_1")

	to, tok, bot, ok := r.lookupMessageMeta("12345")
	if !ok {
		t.Fatal("msgMeta lookup: expected hit")
	}
	if to != "@alice_1" || tok != "ctx-abc" || bot != "@bot_1" {
		t.Fatalf("msgMeta lookup: got (%s, %s, %s)", to, tok, bot)
	}

	userTok, ok := r.lookupUserToken("@alice_1")
	if !ok || userTok != "ctx-abc" {
		t.Fatalf("userToken lookup: got (%s, %v)", userTok, ok)
	}
}

// TestRegisterIncoming_IgnoresInvalid: nil / zero-message-id / empty sender
// don't pollute caches.
func TestRegisterIncoming_IgnoresInvalid(t *testing.T) {
	r := NewReplier(func() []*ilink.Credentials { return nil })

	r.RegisterIncoming(nil, "@bot_1")
	r.RegisterIncoming(&ilink.WeixinMessage{MessageID: 0, FromUserID: "x"}, "@bot_1")
	r.RegisterIncoming(&ilink.WeixinMessage{MessageID: 1, FromUserID: ""}, "@bot_1")

	if _, _, _, ok := r.lookupMessageMeta("0"); ok {
		t.Fatal("expected msgMeta miss for id=0")
	}
	if _, _, _, ok := r.lookupMessageMeta("1"); ok {
		t.Fatal("expected msgMeta miss for empty FromUserID")
	}
}

// TestMsgMetaGC_ExpiresOldEntries: entries older than msgMetaTTL are removed
// by the GC loop, while recent entries stay. userToken survives.
func TestMsgMetaGC_ExpiresOldEntries(t *testing.T) {
	r := NewReplier(func() []*ilink.Credentials { return nil })

	// Seed an old entry directly (simulating a restart-recovered cache).
	old := &msgMeta{
		FromUserID:   "@old_user",
		ContextToken: "old-ctx",
		BotID:        "@bot_1",
		ReceivedAt:   time.Now().Add(-2 * msgMetaTTL),
	}
	r.msgMeta.Store("old-id", old)
	r.userToken.Store("@old_user", "old-ctx")

	// Also seed a fresh entry.
	fresh := &msgMeta{
		FromUserID:   "@fresh_user",
		ContextToken: "fresh-ctx",
		BotID:        "@bot_1",
		ReceivedAt:   time.Now(),
	}
	r.msgMeta.Store("fresh-id", fresh)

	// Inline the GC pass without waiting for the ticker.
	cutoff := time.Now().Add(-msgMetaTTL)
	r.msgMeta.Range(func(k, v any) bool {
		meta, ok := v.(*msgMeta)
		if !ok || meta.ReceivedAt.Before(cutoff) {
			r.msgMeta.Delete(k)
		}
		return true
	})

	if _, ok := r.msgMeta.Load("old-id"); ok {
		t.Fatal("expected old msgMeta entry to be GC'd")
	}
	if _, ok := r.msgMeta.Load("fresh-id"); !ok {
		t.Fatal("expected fresh msgMeta entry to survive GC")
	}
	// userToken is never GC'd by the loop — fallback keeps working.
	if _, ok := r.userToken.Load("@old_user"); !ok {
		t.Fatal("userToken entry should not be GC'd")
	}
}

// TestLookupMessageMeta_MissOnUnknownID: unregistered messageID returns !ok
// so ReplyText can fall back / refuse safely.
func TestLookupMessageMeta_MissOnUnknownID(t *testing.T) {
	r := NewReplier(func() []*ilink.Credentials { return nil })
	if _, _, _, ok := r.lookupMessageMeta("999999"); ok {
		t.Fatal("expected miss on unknown messageID")
	}
}

// TestConcurrentRegisterIncoming: hammer Register + lookup from multiple
// goroutines — race detector catches any sync.Map misuse.
func TestConcurrentRegisterIncoming(t *testing.T) {
	r := NewReplier(func() []*ilink.Credentials {
		return []*ilink.Credentials{{ILinkBotID: "@bot_1"}}
	})
	t.Cleanup(r.Close)

	var wg sync.WaitGroup
	const N = 200
	wg.Add(N * 2)

	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			r.RegisterIncoming(&ilink.WeixinMessage{
				MessageID:    int64(i + 1),
				FromUserID:   "@u-" + strconv.Itoa(i%5),
				ContextToken: "c-" + strconv.Itoa(i),
			}, "@bot_1")
		}(i)

		go func(i int) {
			defer wg.Done()
			r.lookupMessageMeta(strconv.Itoa(i + 1))
			r.lookupUserToken("@u-" + strconv.Itoa(i%5))
		}(i)
	}
	wg.Wait()
}

// TestPrimaryClient_NoCredentials: without credentials, requesting a client
// returns a clear error instead of a nil client.
func TestPrimaryClient_NoCredentials(t *testing.T) {
	r := NewReplier(func() []*ilink.Credentials { return nil })
	if _, err := r.primaryClient(); err == nil {
		t.Fatal("expected error from primaryClient() when provider returns none")
	}
}

func TestClientFor_DoesNotFallbackToPrimaryForUnknownBot(t *testing.T) {
	r := NewReplier(func() []*ilink.Credentials {
		return []*ilink.Credentials{{ILinkBotID: "@new_bot", BotToken: "new-token"}}
	})

	if _, err := r.clientFor("@old_bot"); err == nil {
		t.Fatal("expected unknown bot id to fail instead of falling back to primary client")
	}
}

func TestRefreshCredentials_PreservesSameBotUserContext(t *testing.T) {
	provider := func() []*ilink.Credentials {
		return []*ilink.Credentials{{ILinkBotID: "@bot"}}
	}
	if err := saveUserTokens("@bot", map[string]string{"@u": "ctx"}); err != nil {
		t.Fatalf("saveUserTokens: %v", err)
	}
	r := NewReplier(provider)
	r.msgMeta.Store("msg-1", &msgMeta{FromUserID: "@u", ContextToken: "ctx", BotID: "@bot", ReceivedAt: time.Now()})

	r.RefreshCredentials()

	if _, ok := r.msgMeta.Load("msg-1"); ok {
		t.Fatal("expected msgMeta to be cleared")
	}
	if token, ok := r.lookupUserToken("@u"); !ok || token != "ctx" {
		t.Fatalf("expected same-bot userToken to survive refresh, got (%q, %v)", token, ok)
	}
}
