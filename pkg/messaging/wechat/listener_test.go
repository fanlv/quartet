package wechat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/messaging"
	"github.com/fanlv/quartet/pkg/messaging/wechat/cdn"
	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"
)

func TestExtractContent_Text(t *testing.T) {
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "hello"}},
		},
	}
	if got := extractContent(context.Background(), "1", msg); got != "hello" {
		t.Fatalf("text extract: got %q want %q", got, "hello")
	}
}

func TestExtractContent_Image(t *testing.T) {
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeImage, ImageItem: &ilink.ImageItem{}},
		},
	}
	// With no Media info there is nothing to download → fall back to a
	// markdown-shaped placeholder.
	if got := extractContent(context.Background(), "1", msg); got != "![image](#)" {
		t.Fatalf("image placeholder: got %q", got)
	}
}

func TestExtractContent_VoiceWithTranscription(t *testing.T) {
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeVoice, VoiceItem: &ilink.VoiceItem{Text: "这是语音"}},
		},
	}
	want := "这是语音"
	if got := extractContent(context.Background(), "1", msg); got != want {
		t.Fatalf("voice with transcription: got %q want %q", got, want)
	}
}

func TestExtractContent_VoiceWithoutTranscription(t *testing.T) {
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeVoice, VoiceItem: &ilink.VoiceItem{}},
		},
	}
	if got := extractContent(context.Background(), "1", msg); got != "[voice.mp3](#)" {
		t.Fatalf("voice without transcription: got %q", got)
	}
}

func TestExtractContent_File(t *testing.T) {
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeFile, FileItem: &ilink.FileItem{FileName: "report.pdf"}},
		},
	}
	// Without Media we can't fetch the file, but we still surface the name
	// so prompts can reference it.
	if got := extractContent(context.Background(), "1", msg); got != "[report.pdf](#)" {
		t.Fatalf("file placeholder: got %q", got)
	}
}

func TestExtractContent_FileEmpty(t *testing.T) {
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeFile, FileItem: &ilink.FileItem{}},
		},
	}
	if got := extractContent(context.Background(), "1", msg); got != "[未知文件](#)" {
		t.Fatalf("empty file: got %q", got)
	}
}

func TestExtractContent_Video(t *testing.T) {
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeVideo, VideoItem: &ilink.VideoItem{}},
		},
	}
	// Without Media info the video can't be fetched → plain placeholder.
	if got := extractContent(context.Background(), "1", msg); got != "[video.mp4](#)" {
		t.Fatalf("video placeholder: got %q", got)
	}
}

func TestExtractContent_MultiItem(t *testing.T) {
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "hi"}},
			{Type: ilink.ItemTypeImage, ImageItem: &ilink.ImageItem{}},
		},
	}
	want := "hi\n![image](#)"
	if got := extractContent(context.Background(), "1", msg); got != want {
		t.Fatalf("multi-item: got %q want %q", got, want)
	}
}

func TestExtractContent_UnknownType(t *testing.T) {
	// ItemType=99 is not in the known set; must surface a visible placeholder
	// so the Agent / user_input stream never sees empty Content.
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: 99},
		},
	}
	want := "暂不支持的消息类型：`type=99`"
	if got := extractContent(context.Background(), "1", msg); got != want {
		t.Fatalf("unknown type placeholder: got %q want %q", got, want)
	}
}

func TestExtractContent_UnknownMixedWithText(t *testing.T) {
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "hi"}},
			{Type: 42},
		},
	}
	want := "hi\n暂不支持的消息类型：`type=42`"
	if got := extractContent(context.Background(), "1", msg); got != want {
		t.Fatalf("unknown+text: got %q want %q", got, want)
	}
}

// TestExtractContent_ImageDownloaded exercises the full decrypt-and-save
// path by spinning up a fake CDN endpoint that returns AES-128-ECB-encrypted
// PNG bytes matching the ImageItem's AESKey. The resulting extractContent
// output must include a "![image](...)" reference pointing to a readable
// file under UploadsDir.
func TestExtractContent_ImageDownloaded(t *testing.T) {
	// Arrange: 16-byte AES key; encrypt a minimal PNG magic payload.
	aesKeyHex := "0123456789abcdef0123456789abcdef"
	aesKey, err := hex.DecodeString(aesKeyHex)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}

	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0}
	encrypted, err := cdn.Encrypt(pngMagic, aesKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Fake CDN: respond with the ciphertext regardless of the query param.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/download") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, bytes.NewReader(encrypted))
	}))
	defer server.Close()

	originalCDNBase := cdn.BaseURL
	cdn.BaseURL = server.URL
	t.Cleanup(func() { cdn.BaseURL = originalCDNBase })

	// Point the media cache under a tmp LOCAL_MEMORY so the test stays hermetic.
	tmp := t.TempDir()
	t.Setenv("LOCAL_MEMORY", tmp)

	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{{
			Type: ilink.ItemTypeImage,
			ImageItem: &ilink.ImageItem{
				Media: &ilink.MediaInfo{
					EncryptQueryParam: url.QueryEscape("any-param"),
					EncryptType:       1,
				},
				AESKey: aesKeyHex,
			},
		}},
	}

	got := extractContent(context.Background(), "42", msg)
	const prefix = "![image]("
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, ")") {
		t.Fatalf("unexpected content: %q", got)
	}
	localPath := strings.TrimSuffix(strings.TrimPrefix(got, prefix), ")")
	if filepath.Dir(localPath) != filepath.Join(tmp, "quartet", "data", "uploads", "im-media") {
		t.Fatalf("image saved outside persistent media dir: %s", localPath)
	}
	if filepath.Ext(localPath) != ".png" {
		t.Fatalf("expected .png extension, got %s", localPath)
	}
	sbClient := fileserver.GetFileManager()
	pngResult, err := sbClient.FileRead(&fsmodel.FileReadRequest{File: localPath, Base64: true})
	if err != nil {
		t.Fatalf("read saved image: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString(pngResult.Content)
	if err != nil {
		t.Fatalf("decode saved image: %v", err)
	}
	if !bytes.Equal(data, pngMagic) {
		t.Fatalf("saved bytes mismatch: got %x want %x", data, pngMagic)
	}
}

// TestExtractContent_VideoDownloaded exercises the video decrypt-and-save
// path. The fake CDN returns AES-128-ECB-encrypted bytes whose plaintext
// starts with an MP4 ftyp box, so detectVideoExt picks ".mp4".
func TestExtractContent_VideoDownloaded(t *testing.T) {
	aesKeyHex := "0123456789abcdef0123456789abcdef"
	aesKey, err := hex.DecodeString(aesKeyHex)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}

	// Minimal MP4 ftyp box. http.DetectContentType returns "video/mp4"
	// when the 4-byte type at offset 4 is "ftyp" and the major brand at
	// offset 8 is a known MP4 brand (we use "isom").
	mp4Magic := []byte{
		0x00, 0x00, 0x00, 0x18, // box size = 24
		'f', 't', 'y', 'p',
		'i', 's', 'o', 'm',
		0x00, 0x00, 0x02, 0x00,
		'i', 's', 'o', 'm', 'i', 's', 'o', '2',
	}
	encrypted, err := cdn.Encrypt(mp4Magic, aesKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/download") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, bytes.NewReader(encrypted))
	}))
	defer server.Close()

	originalCDNBase := cdn.BaseURL
	cdn.BaseURL = server.URL
	t.Cleanup(func() { cdn.BaseURL = originalCDNBase })

	tmp := t.TempDir()
	t.Setenv("LOCAL_MEMORY", tmp)

	// VideoItem has no hex AESKey field, so put the base64 key on Media.
	aesKeyBase64 := base64.StdEncoding.EncodeToString(aesKey)
	msg := ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{{
			Type: ilink.ItemTypeVideo,
			VideoItem: &ilink.VideoItem{
				Media: &ilink.MediaInfo{
					EncryptQueryParam: url.QueryEscape("any-video-param"),
					AESKey:            aesKeyBase64,
					EncryptType:       1,
				},
			},
		}},
	}

	got := extractContent(context.Background(), "99", msg)
	const prefix = "[video.mp4](<"
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, ">)") {
		t.Fatalf("unexpected content: %q", got)
	}
	localPath := strings.TrimSuffix(strings.TrimPrefix(got, prefix), ">)")
	if filepath.Dir(localPath) != filepath.Join(tmp, "quartet", "data", "uploads", "im-media") {
		t.Fatalf("video saved outside persistent media dir: %s", localPath)
	}
	if filepath.Ext(localPath) != ".mp4" {
		t.Fatalf("expected .mp4 extension, got %s", localPath)
	}
	sbClient := fileserver.GetFileManager()
	mp4Result, err := sbClient.FileRead(&fsmodel.FileReadRequest{File: localPath, Base64: true})
	if err != nil {
		t.Fatalf("read saved video: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString(mp4Result.Content)
	if err != nil {
		t.Fatalf("decode saved video: %v", err)
	}
	if !bytes.Equal(data, mp4Magic) {
		t.Fatalf("saved bytes mismatch: got %x want %x", data, mp4Magic)
	}
}

func TestItemTypeLabel(t *testing.T) {
	cases := []struct {
		typ  int
		want string
	}{
		{ilink.ItemTypeText, "text"},
		{ilink.ItemTypeImage, "image"},
		{ilink.ItemTypeVoice, "voice"},
		{ilink.ItemTypeFile, "file"},
		{ilink.ItemTypeVideo, "video"},
	}
	for _, tc := range cases {
		msg := ilink.WeixinMessage{ItemList: []ilink.MessageItem{{Type: tc.typ}}}
		if got := itemTypeLabel(msg); got != tc.want {
			t.Fatalf("itemTypeLabel(%d): got %q want %q", tc.typ, got, tc.want)
		}
	}
	// Empty ItemList → unknown
	if got := itemTypeLabel(ilink.WeixinMessage{}); got != "unknown" {
		t.Fatalf("empty ItemList: got %q", got)
	}

	// Mixed items: non-text wins over text regardless of order, so downstream
	// sees "image" (not "text") when the user sent a caption alongside media.
	mixedCases := []struct {
		name  string
		items []int
		want  string
	}{
		{"text+image", []int{ilink.ItemTypeText, ilink.ItemTypeImage}, "image"},
		{"image+text", []int{ilink.ItemTypeImage, ilink.ItemTypeText}, "image"},
		{"text+file", []int{ilink.ItemTypeText, ilink.ItemTypeFile}, "file"},
		{"text+video", []int{ilink.ItemTypeText, ilink.ItemTypeVideo}, "video"},
		{"text+voice", []int{ilink.ItemTypeText, ilink.ItemTypeVoice}, "voice"},
	}
	for _, tc := range mixedCases {
		items := make([]ilink.MessageItem, len(tc.items))
		for i, typ := range tc.items {
			items[i] = ilink.MessageItem{Type: typ}
		}
		msg := ilink.WeixinMessage{ItemList: items}
		if got := itemTypeLabel(msg); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// countingHandler is a minimal messaging.EventHandler test double that
// simply increments a counter on every OnMessage call.
type countingHandler struct {
	calls atomic.Int32
}

func (h *countingHandler) OnMessage(context.Context, *messaging.Message) {
	h.calls.Add(1)
}

func TestHandleIncoming_ContextTokenRefreshOnly(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	provider := func() []*ilink.Credentials {
		return []*ilink.Credentials{{ILinkBotID: "@bot_1"}}
	}
	handler := &countingHandler{}
	replier := NewReplier(provider)
	t.Cleanup(replier.Close)
	l := NewListener(handler, replier, provider)

	l.handleIncoming(context.Background(), nil, "@bot_1", ilink.WeixinMessage{
		MessageID:    42,
		FromUserID:   "@alice_1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx-refreshed",
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: " \ta\n "}},
		},
	})

	if got := handler.calls.Load(); got != 0 {
		t.Fatalf("refresh control message: expected no dispatch, got %d", got)
	}
	if got, ok := replier.lookupUserToken("@alice_1"); !ok || got != "ctx-refreshed" {
		t.Fatalf("refresh control message: in-memory token got (%q, %v)", got, ok)
	}
	if got := loadUserTokens("@bot_1")["@alice_1"]; got != "ctx-refreshed" {
		t.Fatalf("refresh control message: persisted token got %q", got)
	}
}

func TestHandleIncoming_ContextTokenRefreshRequiresStandaloneLowercaseA(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	provider := func() []*ilink.Credentials {
		return []*ilink.Credentials{{ILinkBotID: "@bot_1"}}
	}
	handler := &countingHandler{}
	replier := NewReplier(provider)
	t.Cleanup(replier.Close)
	l := NewListener(handler, replier, provider)

	messages := []ilink.WeixinMessage{
		{
			MessageID:    1,
			FromUserID:   "@alice_1",
			MessageType:  ilink.MessageTypeUser,
			MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{
				{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "A"}},
			},
		},
		{
			MessageID:    2,
			FromUserID:   "@alice_1",
			MessageType:  ilink.MessageTypeUser,
			MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{
				{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "aa"}},
			},
		},
		{
			MessageID:    3,
			FromUserID:   "@alice_1",
			MessageType:  ilink.MessageTypeUser,
			MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{
				{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "a"}},
				{Type: ilink.ItemTypeImage, ImageItem: &ilink.ImageItem{}},
			},
		},
	}

	for _, msg := range messages {
		l.handleIncoming(context.Background(), nil, "@bot_1", msg)
	}

	if got := handler.calls.Load(); got != int32(len(messages)) {
		t.Fatalf("non-control messages: expected %d dispatches, got %d", len(messages), got)
	}
}

// TestHandleIncoming_DedupByMessageID: iLink re-emitting a finish-state
// message (same message_id) must not trigger a second handler dispatch.
// This matches weclaw's seenMsgs behaviour and guards against duplicate
// replies after a monitor reconnect with a stale sync buf.
func TestHandleIncoming_DedupByMessageID(t *testing.T) {
	handler := &countingHandler{}
	replier := NewReplier(func() []*ilink.Credentials { return nil })
	t.Cleanup(replier.Close)
	l := NewListener(handler, replier, func() []*ilink.Credentials { return nil })

	msg := ilink.WeixinMessage{
		MessageID:    42,
		FromUserID:   "@alice_1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "hello"}},
		},
	}

	l.handleIncoming(context.Background(), nil, "@bot_1", msg)
	l.handleIncoming(context.Background(), nil, "@bot_1", msg)

	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("dedup: expected exactly 1 dispatch, got %d", got)
	}
}

// TestHandleIncoming_DistinctMessagesBothDispatch: two different message IDs
// from the same sender must both go through.
func TestHandleIncoming_DistinctMessagesBothDispatch(t *testing.T) {
	handler := &countingHandler{}
	replier := NewReplier(func() []*ilink.Credentials { return nil })
	t.Cleanup(replier.Close)
	l := NewListener(handler, replier, func() []*ilink.Credentials { return nil })

	base := ilink.WeixinMessage{
		FromUserID:   "@alice_1",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "hello"}},
		},
	}
	first := base
	first.MessageID = 1
	second := base
	second.MessageID = 2

	l.handleIncoming(context.Background(), nil, "@bot_1", first)
	l.handleIncoming(context.Background(), nil, "@bot_1", second)

	if got := handler.calls.Load(); got != 2 {
		t.Fatalf("distinct ids: expected 2 dispatches, got %d", got)
	}
}

// TestHandleIncoming_SeenMsgGC_ExpiresOldEntries: the GC pass evicts
// message IDs that have sat in the cache longer than seenMsgTTL, matching
// the replier's msgMeta GC contract.
func TestHandleIncoming_SeenMsgGC_ExpiresOldEntries(t *testing.T) {
	handler := &countingHandler{}
	replier := NewReplier(func() []*ilink.Credentials { return nil })
	l := NewListener(handler, replier, func() []*ilink.Credentials { return nil })

	// Seed an "old" entry directly so we don't have to wait out the ticker.
	l.seenMsgs.Store(int64(1), time.Now().Add(-2*seenMsgTTL))
	l.seenMsgs.Store(int64(2), time.Now())
	l.seenMsgsCount.Store(2)

	l.evictExpired()

	if _, ok := l.seenMsgs.Load(int64(1)); ok {
		t.Fatal("expected old seenMsgs entry to be GC'd")
	}
	if _, ok := l.seenMsgs.Load(int64(2)); !ok {
		t.Fatal("expected fresh seenMsgs entry to survive GC")
	}
	if got := l.seenMsgsCount.Load(); got != 1 {
		t.Fatalf("expected seenMsgsCount to track GC'd entries, got %d", got)
	}
}

// TestCapFileName_UTF8Safe: truncation must never split a multi-byte rune.
// sanitizeFileName preserves Chinese characters (3 bytes each in UTF-8), so a
// long Chinese filename will hit the 200-byte cap. A byte-level slice would
// produce an invalid UTF-8 string that some filesystems reject on os.WriteFile.
func TestCapFileName_UTF8Safe(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
	}{
		{"short ascii", "hello.pdf", 200},
		{"exactly at limit", strings.Repeat("a", 200), 200},
		{"long ascii preserves ext", strings.Repeat("a", 300) + ".pdf", 200},
		{"long chinese preserves ext", strings.Repeat("产", 80) + ".pdf", 200},
		{"long chinese no ext", strings.Repeat("品", 80), 200},
		{"absurdly long ext dropped", strings.Repeat("a", 300) + "." + strings.Repeat("b", 50), 200},
		{"cut lands mid-rune without ext", strings.Repeat("测", 4), 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := messaging.CapFileNameBytes(tc.in, tc.max)
			if len(got) > tc.max {
				t.Fatalf("result exceeds max: len=%d max=%d", len(got), tc.max)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q (bytes=%x)", got, []byte(got))
			}
		})
	}
}

// TestCapFileName_PreservesExtension: when the extension fits, it must stay
// attached to the truncated stem so downstream tools can still dispatch on it.
func TestCapFileName_PreservesExtension(t *testing.T) {
	in := strings.Repeat("产", 80) + ".pdf"
	got := messaging.CapFileNameBytes(in, 200)
	if !strings.HasSuffix(got, ".pdf") {
		t.Fatalf("extension dropped: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q", got)
	}
}
