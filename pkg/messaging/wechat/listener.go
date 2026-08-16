package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
	"github.com/fanlv/quartet/pkg/messaging/media"
	"github.com/fanlv/quartet/pkg/messaging/wechat/cdn"
	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"
	"github.com/fanlv/quartet/pkg/safe"
)

const (
	// seenMsgTTL bounds how long a message_id stays in the dedup cache.
	// iLink re-emits the same finish-state message on reconnect (stale sync
	// buf) and occasionally for voice messages; upstream weclaw uses 5 min.
	seenMsgTTL = 5 * time.Minute
	// seenMsgGCInterval throttles how often expired entries are evicted.
	seenMsgGCInterval = 5 * time.Minute
	// seenMsgMaxEntries is a hard safety cap on the dedup map. The normal
	// eviction path is TTL-based (seenMsgTTL); this bound only kicks in if
	// message volume between two GC ticks runs away. At ~24 B per entry
	// the cap holds ~2.4 MB worst-case.
	seenMsgMaxEntries = 100_000
	// seenMsgEvictMinInterval throttles the hot-path evictExpired call so
	// a storm that keeps the map full of still-valid entries cannot turn
	// every incoming message into an O(N) scan. Once the throttle window
	// is exhausted, a single scan runs; subsequent arrivals skip the
	// scan until the window refreshes.
	seenMsgEvictMinInterval = time.Second
)

// Listener converts iLink WeixinMessage events into platform-neutral
// messaging.Message values and feeds them to the handler. It is a thin shim
// over ilink.Monitor — the actual long-poll loop lives there.
type Listener struct {
	handler       messaging.EventHandler
	replier       *Replier
	credsProvider CredentialsProvider

	// monitor is set once Start() has spun up the long-poll loop. Stored so
	// external callers (Manager.IsExpired → WeChatAccounts API) can peek at
	// the session-expired flag without racing on nil.
	monitor atomic.Pointer[ilink.Monitor]

	// lifecycleCtx captures the ctx passed to Start() so gcLoop can live for
	// the full Listener lifetime. Per-message handler ctxs are short-lived
	// (handlerGracePeriod in ilink.Monitor) and must not be used to start
	// background goroutines — that would tie gcLoop's lifetime to whichever
	// single message happened to be first after a restart.
	lifecycleCtx atomic.Pointer[context.Context]

	// seenMsgs dedups by message_id. iLink may re-push a finish-state
	// message on reconnect (when the persisted sync buf is stale) or for
	// voice transcription updates; without this, the job queue would see
	// the same message multiple times and the user would get duplicate
	// replies. Matches weclaw messaging/handler.go seenMsgs behaviour.
	seenMsgs      sync.Map // int64 → time.Time
	seenMsgsCount atomic.Int64
	// lastEvictNs tracks the last time evictExpired completed (unix nanos).
	// Used by the hot-path cap-hit eviction to avoid re-scanning the full
	// map on every message when nothing in it has aged out yet.
	lastEvictNs atomic.Int64
	gcOnce      sync.Once
}

var _ messaging.Listener = (*Listener)(nil)

// NewListener constructs a Listener. The Replier argument is required: on
// every incoming message the Listener calls replier.RegisterIncoming so
// ReplyText(messageID) can later resolve the ContextToken in O(1).
func NewListener(handler messaging.EventHandler, replier *Replier, credsProvider CredentialsProvider, opts ...Option) *Listener {
	l := &Listener{
		handler:       handler,
		replier:       replier,
		credsProvider: credsProvider,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Start runs the iLink long-poll monitor on the first configured account.
// Returns immediately (nil error) if no credentials are saved — this is the
// expected state before the first scan-to-login. Blocks until ctx is
// cancelled.
func (l *Listener) Start(ctx context.Context) error {
	// Store a heap-allocated copy so startGCOnce can read the listener lifetime
	// context without racing with Start(). Storing &ctx directly would rely on
	// escape analysis of the function parameter; using a copy keeps the pointer's
	// lifetime explicit.
	storedCtx := ctx
	l.lifecycleCtx.Store(&storedCtx)
	creds := l.credsProvider()
	if len(creds) == 0 {
		logger.Info("[wechat] no credentials, waiting for login")
		<-ctx.Done()
		return ctx.Err()
	}

	if len(creds) > 1 {
		for _, extra := range creds[1:] {
			logger.Warn("[wechat] multi-account not supported in v1, ignoring bot_id=%s", extra.ILinkBotID)
		}
	}

	primary := creds[0]
	client := ilink.NewClient(primary)
	botID := primary.ILinkBotID

	monitor, err := ilink.NewMonitor(client, func(ctx context.Context, _ *ilink.Client, msg ilink.WeixinMessage) {
		l.handleIncoming(ctx, client, botID, msg)
	})
	if err != nil {
		return fmt.Errorf("wechat: init monitor: %w", err)
	}
	l.monitor.Store(monitor)

	logger.Info("[wechat] listener started for bot=%s", botID)
	return monitor.Run(ctx)
}

// IsExpired reports whether the underlying monitor has observed an
// unrecoverable session-expired state. Returns false when the listener has
// never been started.
func (l *Listener) IsExpired() bool {
	m := l.monitor.Load()
	return m != nil && m.IsExpired()
}

// handleIncoming processes a single WeixinMessage from the monitor. It first
// registers the message metadata with the replier (so ReplyText can find the
// ContextToken later). A standalone "a" is a token-refresh control message and
// stops there; all other messages are converted and dispatched.
func (l *Listener) handleIncoming(ctx context.Context, client *ilink.Client, botID string, msg ilink.WeixinMessage) {
	// User messages only — ignore bot echoes and generating-state updates.
	if msg.MessageType != ilink.MessageTypeUser {
		logger.Debug("[wechat] skip non-user message: type=%d state=%d",
			msg.MessageType, msg.MessageState)
		return
	}
	if msg.MessageState != ilink.MessageStateFinish {
		logger.Debug("[wechat] skip non-final message: state=%d", msg.MessageState)
		return
	}
	if msg.FromUserID == "" {
		logger.Warn("[wechat] skip message with empty FromUserID: %s", ilink.FormatMessageSummary(msg))
		return
	}

	// Dedup by message_id. iLink re-pushes the same finish-state message
	// after a reconnect with a stale sync buf, and voice messages can
	// trigger multiple finish-state updates. Without this we'd double-reply.
	//
	// MessageID==0 means the upstream didn't assign a stable id — we can't
	// reliably dedup it, so skip the whole message rather than risk sending
	// the same reply twice when iLink replays.
	if msg.MessageID == 0 {
		logger.Warn("[wechat] skip message with zero MessageID: %s", ilink.FormatMessageSummary(msg))
		return
	}
	if _, loaded := l.seenMsgs.LoadOrStore(msg.MessageID, time.Now()); loaded {
		logger.Debug("[wechat] dedup: skip already-seen msg=%d", msg.MessageID)
		return
	}
	// Enforce a hard cap so a message flood between GC ticks cannot grow
	// the dedup map unboundedly. Trigger an early sweep when we cross the
	// threshold — cheaper than the periodic Range in gcLoop because it
	// also evicts just-inserted-but-past-TTL entries that the next tick
	// would clean up anyway.
	if l.seenMsgsCount.Add(1) > seenMsgMaxEntries {
		l.evictExpired()
	}
	l.startGCOnce()

	// Register BEFORE dispatch so ReplyText called synchronously by the
	// handler can still find the ContextToken.
	l.replier.RegisterIncoming(&msg, botID)
	if isContextTokenRefreshMessage(msg) {
		logger.Debugf(ctx, "[wechat] context token refreshed without dispatch: msg=%d from=%s",
			msg.MessageID, msg.FromUserID)
		return
	}

	messageIDStr := strconv.FormatInt(msg.MessageID, 10)
	receivedAt := time.Now()
	content := extractContent(ctx, messageIDStr, msg)
	raw, _ := json.Marshal(msg)

	out := &messaging.Message{
		Platform:    messaging.PlatformWeChat,
		MessageID:   messageIDStr,
		ChatID:      msg.FromUserID, // P2P only; no separate group ID.
		ChatType:    messaging.ChatTypeP2P,
		SenderID:    msg.FromUserID,
		MessageType: itemTypeLabel(msg),
		Content:     content,
		ReceivedAt:  receivedAt,
		EventTime:   receivedAt,
		Mentions:    nil, // iLink P2P TextItem carries no @ info.
		RawEvent:    raw,
	}

	logger.Debugf(ctx, "[wechat] dispatch: msg=%s from=%s type=%s content_len=%d",
		out.MessageID, out.SenderID, out.MessageType, len(out.Content))

	l.handler.OnMessage(ctx, out)
}

func isContextTokenRefreshMessage(msg ilink.WeixinMessage) bool {
	if len(msg.ItemList) != 1 {
		return false
	}
	item := msg.ItemList[0]
	return item.Type == ilink.ItemTypeText &&
		item.TextItem != nil &&
		strings.TrimSpace(item.TextItem.Text) == "a"
}

// extractContent pulls a display string out of the WeChat message item list.
// Text items go through verbatim; image items are downloaded and rendered as
// "![image](/abs/path)". All other non-text items are rendered in the same
// markdown file-link shape: "[foo.pdf](</abs/path>)".
//
// This keeps Content human-readable while making local attachments discoverable
// to downstream prompts. Any download failure degrades to a plain placeholder
// so a broken CDN never swallows the rest of the message.
func extractContent(ctx context.Context, messageID string, msg ilink.WeixinMessage) string {
	if len(msg.ItemList) == 0 {
		return ""
	}

	var parts []string
	for idx, item := range msg.ItemList {
		switch item.Type {
		case ilink.ItemTypeText:
			if item.TextItem != nil && item.TextItem.Text != "" {
				parts = append(parts, item.TextItem.Text)
			}
		case ilink.ItemTypeImage:
			parts = append(parts, renderImageItem(ctx, messageID, idx, item.ImageItem))
		case ilink.ItemTypeVoice:
			// Preserve WeChat's built-in speech-to-text when present. If the audio
			// payload cannot be downloaded, avoid appending a useless [voice](#)
			// marker on top of the transcription; without transcription, keep the
			// placeholder so downstream still sees that a voice message arrived.
			hasTranscript := item.VoiceItem != nil && item.VoiceItem.Text != ""
			if hasTranscript {
				parts = append(parts, item.VoiceItem.Text)
			}
			rendered := renderVoiceItem(ctx, messageID, idx, item.VoiceItem)
			if !hasTranscript || !strings.HasSuffix(rendered, "(#)") {
				parts = append(parts, rendered)
			}
		case ilink.ItemTypeFile:
			parts = append(parts, renderFileItem(ctx, messageID, idx, item.FileItem))
		case ilink.ItemTypeVideo:
			parts = append(parts, renderVideoItem(ctx, messageID, idx, item.VideoItem))
		default:
			// Unknown item types (ItemTypeNone=0, or new iLink-side additions
			// like link cards / mini-programs / stickers). Fall through to a
			// visible placeholder — mirrors pkg/messaging/lark/decoder.go's
			// "暂不支持的消息类型" so downstream never sees empty Content and
			// silently pollutes user_input or feeds an empty user message to
			// the Job.
			logger.Info("[wechat] unsupported item type: msg=%s idx=%d type=%d", messageID, idx, item.Type)
			parts = append(parts, fmt.Sprintf("暂不支持的消息类型：`type=%d`", item.Type))
		}
	}

	return strings.Join(parts, "\n")
}

func renderMarkdownFileLink(name, localPath string) string {
	if name == "" {
		name = "file"
	}
	name = escapeMarkdownLinkText(name)
	if localPath == "" {
		return "[" + name + "](#)"
	}
	// Use <...> so paths with spaces still render as a single URL token.
	return "[" + name + "](<" + localPath + ">)"
}

func escapeMarkdownLinkText(s string) string {
	// Minimal escaping so filenames like "a[b].pdf" don't break the link text.
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `[`, `\[`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	return s
}

// renderImageItem downloads the image payload from the WeChat CDN and returns
// "![image](/abs/path)". On any failure (missing media, decrypt error, disk
// write error) it degrades to a markdown-shaped placeholder "![image](#)".
func renderImageItem(ctx context.Context, messageID string, itemIdx int, img *ilink.ImageItem) string {
	if img == nil || img.Media == nil || img.Media.EncryptQueryParam == "" {
		return "![image](#)"
	}

	// ImageItem.AESKey (raw hex) is preferred over Media.AESKey per the
	// ilink type comment; fall back to the base64 field when absent.
	aesKey := img.Media.AESKey
	if img.AESKey != "" {
		aesKey = cdn.HexDecodedAsBase64(img.AESKey)
	}
	if aesKey == "" {
		return "![image](#)"
	}

	data, err := cdn.Download(ctx, img.Media.EncryptQueryParam, aesKey)
	if err != nil {
		logger.Warn("[wechat] download image failed: msg=%s idx=%d err=%v", messageID, itemIdx, err)
		return "![image](#)"
	}

	ext := detectImageExt(data)
	baseName := fmt.Sprintf("%s_%d_image%s", safeMessageID(messageID), itemIdx, ext)
	localPath, err := writeBytesToUploads(baseName, data)
	if err != nil {
		logger.Warn("[wechat] save image failed: msg=%s idx=%d err=%v", messageID, itemIdx, err)
		return "![image](#)"
	}
	return "![image](" + localPath + ")"
}

// renderFileItem downloads a generic file attachment and returns
// a markdown link "[foo.pdf](</abs/path>)". On failure it degrades to
// "[foo.pdf](#)" so the agent still sees the intended filename.
func renderFileItem(ctx context.Context, messageID string, itemIdx int, f *ilink.FileItem) string {
	name := "未知文件"
	if f != nil && f.FileName != "" {
		name = f.FileName
	}

	if f == nil || f.Media == nil || f.Media.EncryptQueryParam == "" || f.Media.AESKey == "" {
		return renderMarkdownFileLink(name, "")
	}

	data, err := cdn.Download(ctx, f.Media.EncryptQueryParam, f.Media.AESKey)
	if err != nil {
		logger.Warn("[wechat] download file failed: msg=%s idx=%d name=%s err=%v", messageID, itemIdx, name, err)
		return renderMarkdownFileLink(name, "")
	}

	fileName := messaging.CapFileNameBytes(messaging.SanitizeFileNamePart(name), 200)
	if fileName == "" {
		fileName = "file"
	}
	baseName := fmt.Sprintf("%s_%d_%s", safeMessageID(messageID), itemIdx, fileName)
	localPath, err := writeBytesToUploads(baseName, data)
	if err != nil {
		logger.Warn("[wechat] save file failed: msg=%s idx=%d name=%s err=%v", messageID, itemIdx, name, err)
		return renderMarkdownFileLink(name, "")
	}
	return renderMarkdownFileLink(name, localPath)
}

// renderVoiceItem downloads a voice payload from the WeChat CDN and returns a
// markdown link in the form "[voice.mp3](</abs/path>)". Voice items carry no
// filename, so we pick one based on the encode_type. On any failure it
// degrades to "[voice.mp3](#)".
func renderVoiceItem(ctx context.Context, messageID string, itemIdx int, v *ilink.VoiceItem) string {
	ext := voiceExtFromEncodeType(0)
	if v != nil {
		ext = voiceExtFromEncodeType(v.EncodeType)
	}
	name := "voice" + ext

	if v == nil || v.Media == nil || v.Media.EncryptQueryParam == "" || v.Media.AESKey == "" {
		return renderMarkdownFileLink(name, "")
	}

	data, err := cdn.Download(ctx, v.Media.EncryptQueryParam, v.Media.AESKey)
	if err != nil {
		logger.Warn("[wechat] download voice failed: msg=%s idx=%d err=%v", messageID, itemIdx, err)
		return renderMarkdownFileLink(name, "")
	}

	baseName := fmt.Sprintf("%s_%d_voice%s", safeMessageID(messageID), itemIdx, ext)
	localPath, err := writeBytesToUploads(baseName, data)
	if err != nil {
		logger.Warn("[wechat] save voice failed: msg=%s idx=%d err=%v", messageID, itemIdx, err)
		return renderMarkdownFileLink(name, "")
	}
	return renderMarkdownFileLink(name, localPath)
}

func voiceExtFromEncodeType(encodeType int) string {
	// Keep this mapping coarse: we only need a stable extension for downstream
	// tools and humans. When unknown, default to .mp3 (most playable).
	switch encodeType {
	case 1:
		return ".pcm"
	case 2:
		return ".adpcm"
	case 4:
		return ".spx"
	case 5:
		return ".amr"
	case 6:
		return ".silk"
	case 7:
		return ".mp3"
	default:
		return ".mp3"
	}
}

// renderVideoItem downloads a short video clip from the WeChat CDN and returns
// a markdown link in the form "[video.mp4](</abs/path>)". VideoItem carries no
// filename so we sniff the extension from the decrypted bytes. On any failure
// it degrades to "[video.mp4](#)".
func renderVideoItem(ctx context.Context, messageID string, itemIdx int, v *ilink.VideoItem) string {
	if v == nil || v.Media == nil || v.Media.EncryptQueryParam == "" || v.Media.AESKey == "" {
		return renderMarkdownFileLink("video.mp4", "")
	}

	data, err := cdn.Download(ctx, v.Media.EncryptQueryParam, v.Media.AESKey)
	if err != nil {
		logger.Warn("[wechat] download video failed: msg=%s idx=%d err=%v", messageID, itemIdx, err)
		return renderMarkdownFileLink("video.mp4", "")
	}

	ext := detectVideoExt(data)
	baseName := fmt.Sprintf("%s_%d_video%s", safeMessageID(messageID), itemIdx, ext)
	localPath, err := writeBytesToUploads(baseName, data)
	if err != nil {
		logger.Warn("[wechat] save video failed: msg=%s idx=%d err=%v", messageID, itemIdx, err)
		return renderMarkdownFileLink("video.mp4", "")
	}
	return renderMarkdownFileLink("video"+ext, localPath)
}

// detectImageExt sniffs the first bytes of an image to pick a sensible file
// extension. Falls back to ".jpg" — WeChat client compresses to JPEG by
// default so this is the likely-correct bet when DetectContentType returns
// application/octet-stream (e.g. for some HEIC / stickers).
func detectImageExt(data []byte) string {
	ct := http.DetectContentType(data)
	switch {
	case strings.HasPrefix(ct, "image/png"):
		return ".png"
	case strings.HasPrefix(ct, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(ct, "image/gif"):
		return ".gif"
	case strings.HasPrefix(ct, "image/webp"):
		return ".webp"
	case strings.HasPrefix(ct, "image/bmp"):
		return ".bmp"
	}
	return ".jpg"
}

// detectVideoExt sniffs the first bytes of a video payload. Falls back to
// ".mp4" because WeChat re-encodes recorded clips to H.264/MP4 on the client
// before upload — AVI / MKV are rare in the wild. http.DetectContentType
// identifies MP4 as "video/mp4" and MOV as "video/quicktime".
func detectVideoExt(data []byte) string {
	ct := http.DetectContentType(data)
	switch {
	case strings.HasPrefix(ct, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(ct, "video/quicktime"):
		return ".mov"
	case strings.HasPrefix(ct, "video/webm"):
		return ".webm"
	case strings.HasPrefix(ct, "video/x-matroska"):
		return ".mkv"
	case strings.HasPrefix(ct, "video/avi"), strings.HasPrefix(ct, "video/x-msvideo"):
		return ".avi"
	}
	return ".mp4"
}

// writeBytesToUploads persists decrypted CDN bytes under the dedicated IM
// media data directory,
// returning the resulting absolute path. baseName MUST already be sanitized.
func writeBytesToUploads(baseName string, data []byte) (string, error) {
	sb := fileserver.GetFileManager()
	dir, err := media.PersistentDir()
	if err != nil {
		return "", fmt.Errorf("resolve media cache dir failed: %w", err)
	}
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		return "", err
	}
	// Stream the raw bytes via FileUpload so we avoid the
	// base64.StdEncoding.EncodeToString round trip — that copy would push
	// peak memory to ~2.33× the file size for every video / large image.
	destPath := filepath.Join(dir, baseName)
	res, err := sb.FileUpload(baseName, bytes.NewReader(data), destPath)
	if err != nil {
		return "", err
	}
	return res.File, nil
}

// safeMessageID returns a non-empty filesystem-safe form of the iLink
// message ID. iLink usually gives us an int64, but the zero case can happen
// during tests — fall back to "msg" to avoid an awkward "_0_image" prefix.
func safeMessageID(id string) string {
	if id == "" || id == "0" {
		return "msg"
	}
	return id
}

// itemTypeLabel returns a concise type label for messaging.Message.MessageType.
// Non-text items win over text so a mixed [text, image] message surfaces as
// "image" to downstream logs / prompt templates — extractContent already
// carries both parts in Content, but a "text" label would mislead anyone
// scanning MessageType for "did the user send media?".
func itemTypeLabel(msg ilink.WeixinMessage) string {
	hasText := false
	for _, item := range msg.ItemList {
		switch item.Type {
		case ilink.ItemTypeText:
			hasText = true
		case ilink.ItemTypeImage:
			return "image"
		case ilink.ItemTypeVoice:
			return "voice"
		case ilink.ItemTypeFile:
			return "file"
		case ilink.ItemTypeVideo:
			return "video"
		}
	}
	if hasText {
		return "text"
	}
	return "unknown"
}

// startGCOnce launches the seenMsgs GC goroutine lazily. The goroutine stops
// when the Listener's lifecycle ctx (captured in Start) is cancelled —
// important because Manager.Restart replaces the Listener, and a detached
// gcLoop would leak one goroutine per restart. We intentionally do NOT use
// the per-message handler ctx (which carries only the CDN-download grace
// period) since that would kill the GC loop ~60s after the first message.
func (l *Listener) startGCOnce() {
	l.gcOnce.Do(func() {
		ctxPtr := l.lifecycleCtx.Load()
		if ctxPtr == nil {
			// handleIncoming called before Start — impossible in normal
			// wiring but a direct test case might hit it. Fall back to
			// Background so the GC still runs; the test is expected to
			// stop the process anyway.
			bg := context.Background()
			ctxPtr = &bg
		}
		// safe.Go (not bare go) so a gcLoop panic doesn't take the
		// process down; matches the convention used elsewhere in the
		// repo (cmd/web/main.go, handler/im_gateway.go, ...).
		safe.Go(*ctxPtr, func() { l.gcLoop(*ctxPtr) })
	})
}

func (l *Listener) gcLoop(ctx context.Context) {
	ticker := time.NewTicker(seenMsgGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.evictExpired()
		}
	}
}

// evictExpired drops dedup entries older than seenMsgTTL. Called both from
// gcLoop on a fixed ticker and synchronously when seenMsgsCount crosses the
// hard cap, so a message storm between ticks can't grow the map without
// bound. Throttled by seenMsgEvictMinInterval so a flood of hard-cap-hit
// calls from handleIncoming cannot turn into an O(N²) scan when nothing
// in the map has aged out yet.
func (l *Listener) evictExpired() {
	now := time.Now()
	last := l.lastEvictNs.Load()
	if last != 0 && now.UnixNano()-last < int64(seenMsgEvictMinInterval) {
		return
	}
	if !l.lastEvictNs.CompareAndSwap(last, now.UnixNano()) {
		// Another goroutine claimed this eviction slot; let it run.
		return
	}
	cutoff := now.Add(-seenMsgTTL)
	l.seenMsgs.Range(func(k, v any) bool {
		if t, ok := v.(time.Time); ok && t.Before(cutoff) {
			if _, loaded := l.seenMsgs.LoadAndDelete(k); loaded {
				l.seenMsgsCount.Add(-1)
			}
		}
		return true
	})
}
