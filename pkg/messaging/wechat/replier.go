package wechat

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"
	"github.com/fanlv/quartet/pkg/safe"
)

const (
	// msgMetaTTL bounds how long a per-message context entry stays in the
	// cache. After TTL, ReplyText(messageID) refuses to send because the
	// messaging.Replier interface only supplies messageID, not senderID, so it
	// cannot safely fall back to the userToken cache.
	msgMetaTTL = 30 * time.Minute
	// msgMetaGCInterval controls how often the GC goroutine runs.
	msgMetaGCInterval = 10 * time.Minute
)

// msgMeta records everything Replier needs to turn a messageID back into an
// iLink sendmessage call: recipient (FromUserID on the incoming side becomes
// the ToUserID of the reply), the ContextToken for threading, and the bot
// account the message came in on.
type msgMeta struct {
	FromUserID   string
	ContextToken string
	BotID        string
	ReceivedAt   time.Time
}

// Replier implements messaging.Replier for WeChat. It keeps an in-memory
// cache of incoming-message metadata so ReplyText(messageID, ...) can
// resolve the matching FromUserID/ContextToken in O(1) without touching the
// append-only im_message repository.
//
// The cache is populated by RegisterIncoming, called by the Listener on
// every incoming message *before* the messaging.Message is dispatched to
// the handler.
type Replier struct {
	credsProvider CredentialsProvider

	clientsMu sync.RWMutex
	clients   map[string]*ilink.Client // ilink_bot_id → client

	// Message-level context cache: messageID → *msgMeta. Expires after
	// msgMetaTTL. Use sync.Map since this hot path is read-heavy.
	msgMeta sync.Map

	// User-level fallback: fromUserID → latest ContextToken seen from that
	// user. Never expires — one entry per distinct conversation partner.
	userToken sync.Map

	// gcOnce guards the lazy GC goroutine start. In production one Replier
	// lives for the whole process lifetime; Close() exists so tests (and
	// future shutdown paths) can stop the goroutine instead of leaking it.
	gcOnce    sync.Once
	closeOnce sync.Once
	done      chan struct{}
}

var _ messaging.Replier = (*Replier)(nil)
var _ messaging.MediaReplier = (*Replier)(nil)

// NewReplier constructs a Replier that resolves credentials via the given
// provider. The provider is called lazily so scan-to-login can wire in new
// credentials without re-constructing the Replier. The persisted user-token
// map (see token_store.go) is loaded eagerly so proactive SendText calls
// keep working across restarts.
func NewReplier(credsProvider CredentialsProvider) *Replier {
	r := &Replier{
		credsProvider: credsProvider,
		clients:       make(map[string]*ilink.Client),
		done:          make(chan struct{}),
	}
	for fromUserID, token := range loadUserTokens() {
		if fromUserID != "" && token != "" {
			r.userToken.Store(fromUserID, token)
		}
	}
	return r
}

// RegisterIncoming records the metadata needed to reply to msg later. Called
// by the Listener for every incoming message. msg.ContextToken can be empty
// (iLink may drop it for some message types); reply logic already handles
// the empty-token case by falling back to a fresh client ID.
func (r *Replier) RegisterIncoming(msg *ilink.WeixinMessage, botID string) {
	if msg == nil || msg.MessageID == 0 || msg.FromUserID == "" {
		return
	}

	meta := &msgMeta{
		FromUserID:   msg.FromUserID,
		ContextToken: msg.ContextToken,
		BotID:        botID,
		ReceivedAt:   time.Now(),
	}
	r.msgMeta.Store(strconv.FormatInt(msg.MessageID, 10), meta)
	if msg.ContextToken != "" {
		r.userToken.Store(msg.FromUserID, msg.ContextToken)
		r.persistUserTokens()
	}

	r.startGCOnce()
}

// SendText proactively sends a text message to the given iLink user ID. Used
// for scheduled jobs / loop completions (not currently wired up but exposed
// per doc §9.2). Requires a prior incoming message from toUserID so we have
// a ContextToken on hand — errors if no history exists.
func (r *Replier) SendText(ctx context.Context, toUserID, content string) error {
	contextToken, ok := r.lookupUserToken(toUserID)
	if !ok {
		return fmt.Errorf("wechat: no context for user %s (user must message bot first)", toUserID)
	}

	client, err := r.primaryClient()
	if err != nil {
		return err
	}

	return sendText(ctx, client, toUserID, content, contextToken)
}

// ReplyText replies to the message with the given messageID. Looks up the
// target user and ContextToken via the msgMeta cache populated by the
// listener's RegisterIncoming. On a miss (message older than msgMetaTTL, or
// received before a quartet restart), refuses to send and returns an
// error — silently addressing the wrong user is worse than dropping the
// reply, and the messaging.Replier interface does not give us enough
// information (no senderID) to safely fall back to userToken. Callers that
// know the sender can use SendText(toUserID, content) instead.
func (r *Replier) ReplyText(ctx context.Context, messageID, content string) error {
	toUserID, contextToken, botID, ok := r.lookupMessageMeta(messageID)
	if !ok {
		return fmt.Errorf("wechat: reply aborted — no context for msg=%s", messageID)
	}

	client, err := r.clientFor(botID)
	if err != nil {
		return err
	}

	if err := sendText(ctx, client, toUserID, content, contextToken); err != nil {
		return err
	}

	// Refresh userToken cache with the latest context (though the server
	// typically returns the same token for an ongoing conversation).
	if contextToken != "" {
		r.userToken.Store(toUserID, contextToken)
		r.persistUserTokens()
	}
	return nil
}

// ReplyMedia replies to the message with a native WeChat media attachment.
// The messageID lookup mirrors ReplyText so media replies are sent to the same
// conversation/thread as the triggering incoming message.
func (r *Replier) ReplyMedia(ctx context.Context, messageID string, media messaging.MediaPayload) error {
	toUserID, contextToken, botID, ok := r.lookupMessageMeta(messageID)
	if !ok {
		return fmt.Errorf("wechat: media reply aborted — no context for msg=%s", messageID)
	}

	client, err := r.clientFor(botID)
	if err != nil {
		return err
	}

	switch {
	case media.URL != "":
		err = sendMediaFromURL(ctx, client, toUserID, media.URL, contextToken)
	case media.Path != "":
		err = sendMediaFromPath(ctx, client, toUserID, media.Path, contextToken)
	default:
		err = fmt.Errorf("wechat: empty media payload")
	}
	if err != nil {
		return err
	}

	if contextToken != "" {
		r.userToken.Store(toUserID, contextToken)
		r.persistUserTokens()
	}
	return nil
}

// lookupMessageMeta resolves the reply target from a messageID. Returns
// (toUserID, contextToken, botID, found).
func (r *Replier) lookupMessageMeta(messageID string) (string, string, string, bool) {
	if v, ok := r.msgMeta.Load(messageID); ok {
		meta := v.(*msgMeta)
		return meta.FromUserID, meta.ContextToken, meta.BotID, true
	}
	// msgMeta miss — the message may have been received before a restart,
	// or expired from cache. We have no way to recover FromUserID from just
	// messageID at this point; callers must fall back to user-level lookup
	// via lookupUserToken if they know the sender.
	return "", "", "", false
}

// lookupUserToken returns the latest ContextToken seen from fromUserID.
func (r *Replier) lookupUserToken(fromUserID string) (string, bool) {
	v, ok := r.userToken.Load(fromUserID)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// persistUserTokens snapshots the userToken map to disk so proactive sends
// (SendText) keep working after a backend restart. Best-effort: a failed
// write only reverts to the pre-persistence behavior, so it logs and moves
// on rather than failing the reply that triggered it.
func (r *Replier) persistUserTokens() {
	tokens := make(map[string]string)
	r.userToken.Range(func(k, v any) bool {
		key, kok := k.(string)
		token, vok := v.(string)
		if kok && vok && key != "" && token != "" {
			tokens[key] = token
		}
		return true
	})
	if err := saveUserTokens(tokens); err != nil {
		logger.Warn("[wechat/replier] persist user tokens failed: %v", err)
	}
}

// clientFor returns the ilink.Client for the given bot ID. A non-empty botID
// must match current credentials; otherwise a delayed reply from an old login
// session could be sent through the new primary account. Empty botID is kept as
// the only legacy path that falls back to the primary client.
func (r *Replier) clientFor(botID string) (*ilink.Client, error) {
	if botID == "" {
		return r.primaryClient()
	}

	r.clientsMu.RLock()
	if c, ok := r.clients[botID]; ok {
		r.clientsMu.RUnlock()
		return c, nil
	}
	r.clientsMu.RUnlock()

	var matched *ilink.Credentials
	for _, c := range r.credsProvider() {
		if c != nil && c.ILinkBotID == botID {
			matched = c
			break
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("wechat: no credentials for bot %s", botID)
	}

	r.clientsMu.Lock()
	defer r.clientsMu.Unlock()
	if existing, ok := r.clients[botID]; ok {
		return existing, nil
	}
	client := ilink.NewClient(matched)
	r.clients[botID] = client
	return client, nil
}

// primaryClient returns the client for the first credential returned by the
// provider, building it lazily on first use. Multi-account support (doc §4.5)
// is deferred.
func (r *Replier) primaryClient() (*ilink.Client, error) {
	creds := r.credsProvider()
	if len(creds) == 0 {
		return nil, fmt.Errorf("wechat: no credentials configured")
	}

	c := creds[0]

	r.clientsMu.Lock()
	defer r.clientsMu.Unlock()

	if existing, ok := r.clients[c.ILinkBotID]; ok {
		return existing, nil
	}
	client := ilink.NewClient(c)
	r.clients[c.ILinkBotID] = client
	return client, nil
}

// ResetClients drops cached ilink.Clients and per-session reply metadata so
// the next ReplyText builds fresh clients from the current credentials. Called
// from Manager.Restart() after login/logout so stale bot tokens and ContextToken
// values are not reused by delayed agent replies from the previous session.
func (r *Replier) ResetClients() {
	r.clientsMu.Lock()
	defer r.clientsMu.Unlock()
	r.clients = make(map[string]*ilink.Client)
	clearSyncMap(&r.msgMeta)
	clearSyncMap(&r.userToken)
}

func clearSyncMap(m *sync.Map) {
	m.Range(func(k, _ any) bool {
		m.Delete(k)
		return true
	})
}

// startGCOnce launches the background GC goroutine (only once). The goroutine
// removes msgMeta entries older than msgMetaTTL every msgMetaGCInterval.
// userToken entries are not GC'd — one entry per distinct conversation
// partner is bounded enough in practice.
func (r *Replier) startGCOnce() {
	r.gcOnce.Do(func() {
		safe.Go(context.Background(), r.gcLoop)
	})
}

func (r *Replier) gcLoop() {
	ticker := time.NewTicker(msgMetaGCInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-msgMetaTTL)
			r.msgMeta.Range(func(k, v any) bool {
				meta, ok := v.(*msgMeta)
				if !ok || meta.ReceivedAt.Before(cutoff) {
					r.msgMeta.Delete(k)
				}
				return true
			})
		}
	}
}

// Close stops the background msgMeta GC goroutine. Safe to call multiple
// times; safe to skip entirely in production (the goroutine is cleaned up
// when the process exits). Tests use t.Cleanup(r.Close) to avoid leaking
// one goroutine per test case.
func (r *Replier) Close() {
	r.closeOnce.Do(func() {
		close(r.done)
	})
}

// sendText wraps ilink.SendMessageRequest with a single TextItem. Kept
// separate from SendText/ReplyText so both can share the code without the
// Replier having to care about iLink field names.
func sendText(ctx context.Context, client *ilink.Client, toUserID, content, contextToken string) error {
	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   client.BotID(),
			ToUserID:     toUserID,
			ClientID:     newClientID(),
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{
				{
					Type:     ilink.ItemTypeText,
					TextItem: &ilink.TextItem{Text: content},
				},
			},
			ContextToken: contextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}

	resp, err := client.SendMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("wechat: send message: %w", err)
	}
	if resp.Ret != 0 {
		return fmt.Errorf("wechat: send message failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
	}

	logger.Debugf(ctx, "[wechat/replier] sent reply to %s (%d chars)", toUserID, len(content))
	return nil
}
