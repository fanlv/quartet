package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fanlv/quartet/pkg/messaging"
	"github.com/fanlv/quartet/services/agent/catalog"
	"github.com/fanlv/quartet/services/config"
	imservice "github.com/fanlv/quartet/services/im"
	jobsvc "github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/model"
)

// --- test doubles ---------------------------------------------------------

// fakeSettings is a minimal config.SettingsService for testing the gateway's
// platform-aware lookups. Only the methods touched by dispatchMessage +
// isAdminSender + hasAdminConfigured are meaningfully implemented; the rest
// return zero values.
type fakeSettings struct {
	mu             sync.Mutex
	larkAdmin      string
	larkSophia     string
	wechatAdminIDs []string
}

var _ config.SettingsService = (*fakeSettings)(nil)

func (f *fakeSettings) GetSettings() (*model.Settings, error) {
	return &model.Settings{}, nil
}
func (f *fakeSettings) SaveSettings(*model.Settings) error                    { return nil }
func (f *fakeSettings) SaveTitleGenerationAgent(*model.AgentRoleConfig) error { return nil }
func (f *fakeSettings) SaveGroupReplyAgent(*model.AgentRoleConfig) error      { return nil }
func (f *fakeSettings) SaveIMSessionAgent(*model.IMSessionAgentConfig) error {
	return nil
}
func (f *fakeSettings) SaveACPEnvVars(string, []model.ACPEnvVarEntry) (int64, bool, error) {
	return 0, false, nil
}
func (f *fakeSettings) StageACPEnvVars(string, []model.ACPEnvVarEntry) (int64, error) {
	return 0, nil
}
func (f *fakeSettings) RestoreACPEnvState(string, int64, []model.ACPEnvVarEntry, int64) error {
	return nil
}
func (f *fakeSettings) SaveAgentPrefs(string, model.AgentPrefs) error { return nil }
func (f *fakeSettings) ClearAgentSettings(string) error               { return nil }
func (f *fakeSettings) GetACPEnvVars(string) map[string]string        { return nil }
func (f *fakeSettings) GetACPEnvVersion(string) int64                 { return 0 }
func (f *fakeSettings) GetLarkConfig() (string, string)               { return "", "" }
func (f *fakeSettings) GetLarkIMSenderIDs() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.larkAdmin, f.larkSophia
}
func (f *fakeSettings) GetIMConfig() (string, *model.IMSessionAgentConfig) { return "", nil }
func (f *fakeSettings) GetGroupReplyAgent() *model.AgentRoleConfig         { return nil }
func (f *fakeSettings) GetWeChatAdminIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.wechatAdminIDs))
	copy(out, f.wechatAdminIDs)
	return out
}
func (f *fakeSettings) AddWeChatAdminID(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.wechatAdminIDs {
		if existing == id {
			return nil
		}
	}
	f.wechatAdminIDs = append(f.wechatAdminIDs, id)
	return nil
}
func (f *fakeSettings) RemoveWeChatAdminID(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	filtered := f.wechatAdminIDs[:0]
	for _, existing := range f.wechatAdminIDs {
		if existing == id {
			continue
		}
		filtered = append(filtered, existing)
	}
	f.wechatAdminIDs = filtered
	return nil
}

// fakeReplier records reply targets so assertions can verify routing.
type fakeReplier struct {
	mu       sync.Mutex
	replies  []string
	failWith error
}

func (r *fakeReplier) SendText(context.Context, string, string) error { return nil }
func (r *fakeReplier) ReplyText(_ context.Context, messageID, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return r.failWith
	}
	r.replies = append(r.replies, messageID+"="+content)
	return nil
}

type fakeMediaReplier struct {
	fakeReplier
	mediaFailWith error
	mediaReplies  []string
}

func (r *fakeMediaReplier) ReplyMedia(_ context.Context, messageID string, media messaging.MediaPayload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mediaFailWith != nil {
		return r.mediaFailWith
	}
	if media.URL != "" {
		r.mediaReplies = append(r.mediaReplies, messageID+"=url:"+media.URL)
		return nil
	}
	r.mediaReplies = append(r.mediaReplies, messageID+"=path:"+media.Path)
	return nil
}

type imTaskSettings struct {
	config.SettingsService
	workspaceID string
	agent       *model.IMSessionAgentConfig
}

func (s *imTaskSettings) GetIMConfig() (string, *model.IMSessionAgentConfig) {
	return s.workspaceID, s.agent
}

type imTaskStore struct {
	imservice.Store
	mapping *model.IMJobMapping
}

func (s *imTaskStore) GetJobMapping(string, string) (*model.IMJobMapping, error) {
	return s.mapping, nil
}

func (*imTaskStore) SaveJobMapping(*model.IMJobMapping) error { return nil }

func (*imTaskStore) AppendMessage(context.Context, *model.IMMessage) error { return nil }

type imTaskJobService struct {
	jobsvc.Service
	job         *model.Job
	createdJobs int
}

func (s *imTaskJobService) CreateIdempotent(job *model.Job) (*model.Job, bool, error) {
	if s.job != nil && s.job.ID == job.ID {
		if s.job.CreationPayloadHash != job.CreationPayloadHash {
			return nil, false, jobsvc.ErrClientMessageIDConflict
		}
		return s.job.DeepCopy(), true, nil
	}
	s.job = job.DeepCopy()
	s.createdJobs++
	return s.job.DeepCopy(), false, nil
}

func (s *imTaskJobService) UpdateTitle(string, string) error { return nil }

func (s *imTaskJobService) Get(jobID string) (*model.Job, bool) {
	if s.job == nil || s.job.ID != jobID {
		return nil, false
	}
	return s.job, true
}

// --- tests ---------------------------------------------------------------

func newTestGateway(t *testing.T, fs *fakeSettings) *imGateway {
	t.Helper()
	h := &Handler{settingsService: fs}
	return &imGateway{
		h:              h,
		repliers:       make(map[messaging.Platform]messaging.Replier),
		jobQueues:      make(map[string]*imJobQueue),
		chatDispatches: make(map[string]*imChatDispatcher),
	}
}

func TestIMClientMessageIDStableAndSourceScoped(t *testing.T) {
	base := &messaging.Message{
		Platform:  messaging.PlatformLark,
		ChatID:    "chat-1",
		MessageID: "message-1",
		Content:   "hello",
	}
	first := imClientMessageID(base)
	if first == "" {
		t.Fatal("stable IM clientMessageId is empty")
	}
	if createID := imCreateJobClientMessageID(base); createID == first {
		t.Fatalf("message and create-job namespaces collided: %q", first)
	}
	redelivery := *base
	redelivery.ParentID = "changed-parent"
	redelivery.Content = "content decoded differently on redelivery"
	redelivery.RawEvent = []byte(`{"delivery":2}`)
	if got := imClientMessageID(&redelivery); got != first {
		t.Fatalf("redelivery id=%q, want %q", got, first)
	}

	differentChat := *base
	differentChat.ChatID = "chat-2"
	if got := imClientMessageID(&differentChat); got == first {
		t.Fatalf("different chat collided: %q", got)
	}
	differentPlatform := *base
	differentPlatform.Platform = messaging.PlatformWeChat
	if got := imClientMessageID(&differentPlatform); got == first {
		t.Fatalf("different platform collided: %q", got)
	}
}

func TestIMClientMessageIDLengthPrefixAvoidsSeparatorCollision(t *testing.T) {
	left := &messaging.Message{
		Platform:  messaging.Platform("lark"),
		ChatID:    "chat:part",
		MessageID: "message",
	}
	right := &messaging.Message{
		Platform:  messaging.Platform("lark:chat"),
		ChatID:    "part",
		MessageID: "message",
	}

	// A plain separator join would encode both tuples as the same string.
	if strings.Join([]string{string(left.Platform), left.ChatID, left.MessageID}, ":") !=
		strings.Join([]string{string(right.Platform), right.ChatID, right.MessageID}, ":") {
		t.Fatal("test fixture does not reproduce separator ambiguity")
	}
	if leftID, rightID := imClientMessageID(left), imClientMessageID(right); leftID == rightID {
		t.Fatalf("ambiguous tuples collided: %q", leftID)
	}
}

func TestIMClientMessageIDHasSafeFixedLengthFormat(t *testing.T) {
	msg := &messaging.Message{
		Platform:  messaging.Platform(strings.Repeat("platform:/? ", 100)),
		ChatID:    strings.Repeat("聊天/with spaces?", 100),
		MessageID: strings.Repeat("message:#%", 100),
	}

	got := imClientMessageID(msg)
	if want := len("im-") + sha256.Size*2; len(got) != want {
		t.Fatalf("clientMessageId length=%d, want %d: %q", len(got), want, got)
	}
	if !strings.HasPrefix(got, "im-") {
		t.Fatalf("clientMessageId prefix is not safe namespace: %q", got)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(got, "im-")); err != nil {
		t.Fatalf("clientMessageId payload is not lowercase-safe hex: %q: %v", got, err)
	}
	if got != strings.ToLower(got) {
		t.Fatalf("clientMessageId contains uppercase characters: %q", got)
	}
}

func TestIMClientMessageIDIgnoresDownloadedImagePath(t *testing.T) {
	firstDelivery := &messaging.Message{
		Platform:  messaging.PlatformLark,
		ChatID:    "chat-image",
		MessageID: "message-image",
		Content:   "请看 ![image](/tmp/quartet/images/first.png)",
	}
	redelivery := *firstDelivery
	redelivery.Content = "请看 ![image](/another/cache/root/second.png)"

	if got, want := imClientMessageID(&redelivery), imClientMessageID(firstDelivery); got != want {
		t.Fatalf("downloaded image path changed clientMessageId: got %q, want %q", got, want)
	}
}

func TestBuildQueuedJobTaskWiresIMClientMessageID(t *testing.T) {
	const (
		workspaceID = "workspace-existing"
		jobID       = "job-existing"
	)
	msg := &messaging.Message{
		Platform:  messaging.PlatformWeChat,
		ChatID:    "chat-existing",
		MessageID: "message-existing",
		Content:   "hello from IM",
	}
	mapping := &model.IMJobMapping{
		Platform:    string(msg.Platform),
		ChatID:      msg.ChatID,
		WorkspaceID: workspaceID,
		JobID:       jobID,
	}
	g := &imGateway{
		h: &Handler{
			settingsService: &imTaskSettings{agent: &model.IMSessionAgentConfig{AgentID: "codex"}},
			agentCatalog:    new(catalog.Service),
			jobService:      &imTaskJobService{job: &model.Job{ID: jobID}},
		},
		store: &imTaskStore{mapping: mapping},
	}

	task, err := g.buildQueuedJobTask(context.Background(), msg)
	if err != nil {
		t.Fatalf("buildQueuedJobTask failed: %v", err)
	}
	if task == nil || task.req == nil {
		t.Fatal("buildQueuedJobTask returned a nil task/request")
	}
	if got, want := task.req.ClientMessageID, imClientMessageID(msg); got != want {
		t.Fatalf("request clientMessageId=%q, want %q", got, want)
	}
	if task.req.ClientMessageID == msg.MessageID {
		t.Fatalf("request used raw provider message ID without source namespace: %q", task.req.ClientMessageID)
	}
}

func TestResolveJobRedeliveryReusesIdempotentlyCreatedJob(t *testing.T) {
	workdir := t.TempDir()
	msg := &messaging.Message{Platform: messaging.PlatformLark, ChatID: "chat-new", MessageID: "message-new"}
	jobs := &imTaskJobService{}
	g := &imGateway{
		h: &Handler{
			workspaceService:  &createJobWorkspaceService{workspace: &model.Workspace{ID: "ws-1", Workdir: workdir}},
			jobService:        jobs,
			recentDirsService: createJobRecentDirsService{},
			settingsService:   &imTaskSettings{workspaceID: "ws-1"},
		},
	}
	config := &model.IMSessionAgentConfig{ModelID: "model-1"}

	first, _, err := g.resolveJob(context.Background(), msg, nil, config, "codex")
	if err != nil {
		t.Fatalf("first resolveJob: %v", err)
	}
	second, _, err := g.resolveJob(context.Background(), msg, nil, config, "codex")
	if err != nil {
		t.Fatalf("redelivery resolveJob: %v", err)
	}
	if first.ID == "" || second.ID != first.ID {
		t.Fatalf("redelivery jobs=(%q,%q), want same non-empty ID", first.ID, second.ID)
	}
	if jobs.createdJobs != 1 {
		t.Fatalf("created jobs=%d, want 1", jobs.createdJobs)
	}
	if first.CreationClientMessageID != imCreateJobClientMessageID(msg) {
		t.Fatalf("creation clientMessageId=%q, want %q", first.CreationClientMessageID, imCreateJobClientMessageID(msg))
	}
	if first.CreationClientMessageID == imClientMessageID(msg) {
		t.Fatal("IM create and Agent message idempotency namespaces must differ")
	}
}

// TestRegisterReplier_RouteByPlatform: registering two different platforms
// and reading them back returns the right replier for each. Also verifies
// concurrent RegisterReplier + replier(p) is race-free.
func TestRegisterReplier_RouteByPlatform(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})

	lark := &fakeReplier{}
	wx := &fakeReplier{}
	g.RegisterReplier(messaging.PlatformLark, lark)
	g.RegisterReplier(messaging.PlatformWeChat, wx)

	if g.replier(messaging.PlatformLark) != lark {
		t.Fatalf("expected lark replier")
	}
	if g.replier(messaging.PlatformWeChat) != wx {
		t.Fatalf("expected wechat replier")
	}
	if g.replier(messaging.Platform("unknown")) != nil {
		t.Fatalf("expected nil for unknown platform")
	}

	// Hammer Register + read from many goroutines.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); g.RegisterReplier(messaging.PlatformWeChat, wx) }()
		go func() { defer wg.Done(); _ = g.replier(messaging.PlatformWeChat) }()
	}
	wg.Wait()
}

// TestReplyToMessage_RoutesByPlatform: replyToMessage picks the replier
// matching msg.Platform.
func TestReplyToMessage_RoutesByPlatform(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	lark := &fakeReplier{}
	wx := &fakeReplier{}
	g.RegisterReplier(messaging.PlatformLark, lark)
	g.RegisterReplier(messaging.PlatformWeChat, wx)

	g.replyToMessage(context.Background(), messaging.PlatformLark, "lark-msg", "hi-lark")
	g.replyToMessage(context.Background(), messaging.PlatformWeChat, "wx-msg", "hi-wx")

	if len(lark.replies) != 1 || lark.replies[0] != "lark-msg=hi-lark" {
		t.Fatalf("lark replies: %v", lark.replies)
	}
	if len(wx.replies) != 1 || wx.replies[0] != "wx-msg=hi-wx" {
		t.Fatalf("wechat replies: %v", wx.replies)
	}
}

// TestReplyToMessage_MissingReplier_NoPanic: platforms without a registered
// replier silently log and skip — must not panic.
func TestReplyToMessage_MissingReplier_NoPanic(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	g.replyToMessage(context.Background(), messaging.PlatformWeChat, "m", "c")
}

// TestReplyToMessage_ErrorSwallowed: Replier errors are logged but don't
// propagate (ReplyText returns nil to caller).
func TestReplyToMessage_ErrorSwallowed(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	g.RegisterReplier(messaging.PlatformLark, &fakeReplier{failWith: errors.New("boom")})
	// Must not panic / crash the goroutine.
	g.replyToMessage(context.Background(), messaging.PlatformLark, "m", "c")
}

// TestReplyToMessage_SplitsLongContent: oversize content is chunked into
// multiple ReplyText calls, each under the platform chunk cap.
func TestReplyToMessage_SplitsLongContent(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	fr := &fakeReplier{}
	g.RegisterReplier(messaging.PlatformLark, fr)

	// Build content with deterministic newlines so we can assert paragraph
	// boundaries stay intact. 3 paragraphs of ~5KB each => must split into
	// ≥ 2 chunks, and each chunk ≤ maxLarkReplyChunkBytes.
	para := strings.Repeat("a", 5000) + "\n"
	long := para + para + para

	g.replyToMessage(context.Background(), messaging.PlatformLark, "m", long)

	if len(fr.replies) < 2 {
		t.Fatalf("expected content to be split into ≥2 chunks, got %d", len(fr.replies))
	}
	for i, entry := range fr.replies {
		// fakeReplier stores "messageID=content"; strip the prefix.
		content := strings.TrimPrefix(entry, "m=")
		if len(content) > maxLarkReplyChunkBytes {
			t.Fatalf("chunk %d exceeds cap: %d > %d", i, len(content), maxLarkReplyChunkBytes)
		}
	}
	// Concatenating all chunks must round-trip the original content.
	var rejoined strings.Builder
	for _, entry := range fr.replies {
		rejoined.WriteString(strings.TrimPrefix(entry, "m="))
	}
	if rejoined.String() != long {
		t.Fatalf("round-trip mismatch: got %d bytes want %d bytes", rejoined.Len(), len(long))
	}
}

func TestReplyToMessage_UsesWeChatChunkCap(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	fr := &fakeReplier{}
	g.RegisterReplier(messaging.PlatformWeChat, fr)

	long := strings.Repeat("a", maxWeChatReplyChunkBytes+500)
	g.replyToMessage(context.Background(), messaging.PlatformWeChat, "m", long)

	if len(fr.replies) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(fr.replies))
	}
	for i, entry := range fr.replies {
		content := strings.TrimPrefix(entry, "m=")
		if len(content) > maxWeChatReplyChunkBytes {
			t.Fatalf("chunk %d exceeds wechat cap: %d > %d", i, len(content), maxWeChatReplyChunkBytes)
		}
	}
}

func TestReplyToMessage_WeChatMediaAwareReply(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	fr := &fakeMediaReplier{}
	g.RegisterReplier(messaging.PlatformWeChat, fr)

	g.replyToMessage(context.Background(), messaging.PlatformWeChat, "m", "before\n![image](https://example.com/a.png)\nafter")

	if len(fr.mediaReplies) != 1 || fr.mediaReplies[0] != "m=url:https://example.com/a.png" {
		t.Fatalf("media replies: %v", fr.mediaReplies)
	}
	if len(fr.replies) != 2 || fr.replies[0] != "m=before\n" || fr.replies[1] != "m=\nafter" {
		t.Fatalf("text replies: %v", fr.replies)
	}
}

func TestReplyToMessage_WeChatMediaFallbackOnError(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	fr := &fakeMediaReplier{mediaFailWith: errors.New("upload failed")}
	g.RegisterReplier(messaging.PlatformWeChat, fr)
	allowedDir := t.TempDir()
	t.Setenv("LOCAL_MEMORY", allowedDir)
	mediaPath := filepath.Join(allowedDir, "a.png")

	g.replyToMessage(context.Background(), messaging.PlatformWeChat, "m", "![image]("+mediaPath+")")

	if len(fr.mediaReplies) != 0 {
		t.Fatalf("media replies should be empty on failure: %v", fr.mediaReplies)
	}
	if len(fr.replies) != 1 || fr.replies[0] != "m=![image]("+mediaPath+")" {
		t.Fatalf("fallback text replies: %v", fr.replies)
	}
}

func TestSplitMediaReplyParts_LocalPathRequiresAllowlist(t *testing.T) {
	allowedDir := t.TempDir()
	outsideDir := t.TempDir()
	t.Setenv("LOCAL_MEMORY", allowedDir)

	allowedPath := filepath.Join(allowedDir, "image.png")
	parts, ok := splitMediaReplyParts("![image](" + allowedPath + ")")
	if !ok || len(parts) != 1 || parts[0].media.Path != allowedPath {
		t.Fatalf("expected allowed local path to become media payload, ok=%v parts=%#v", ok, parts)
	}

	outsidePath := filepath.Join(outsideDir, "secret.png")
	parts, ok = splitMediaReplyParts("![image](" + outsidePath + ")")
	if ok {
		t.Fatalf("expected outside local path to stay as text, parts=%#v", parts)
	}
	if len(parts) != 1 || parts[0].text != "![image]("+outsidePath+")" {
		t.Fatalf("outside path should be preserved as fallback text, parts=%#v", parts)
	}
}

func TestSplitMediaReplyParts_IgnoresUnsupportedTargets(t *testing.T) {
	parts, ok := splitMediaReplyParts("![image](#) and ![image](relative.png)")
	if ok {
		t.Fatal("expected no native media payload for unsupported targets")
	}
	if len(parts) != 1 || parts[0].text != "![image](#) and ![image](relative.png)" {
		t.Fatalf("parts: %#v", parts)
	}
}

// TestSplitReplyContent_ShortContent: content under the cap returns a
// single-element slice with the exact input.
func TestSplitReplyContent_ShortContent(t *testing.T) {
	out := splitReplyContent("hello world", 100)
	if len(out) != 1 || out[0] != "hello world" {
		t.Fatalf("expected single chunk with original content, got %v", out)
	}
}

// TestSplitReplyContent_NewlineBreak: when a newline exists in the last
// quarter of the budget, the splitter prefers it over a mid-line cut.
func TestSplitReplyContent_NewlineBreak(t *testing.T) {
	// 100-byte budget. Put a newline at byte 80 (within the last quarter),
	// then a long tail. Expect first chunk to end at the newline.
	content := strings.Repeat("a", 80) + "\n" + strings.Repeat("b", 200)
	chunks := splitReplyContent(content, 100)
	if len(chunks) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(chunks))
	}
	if !strings.HasSuffix(chunks[0], "\n") {
		t.Fatalf("first chunk should end at newline, got suffix %q", chunks[0][len(chunks[0])-1:])
	}
}

// TestSplitReplyContent_UTF8Safe: splitting never cuts a multi-byte rune.
// Chinese characters are 3 bytes in UTF-8; ensure every chunk is valid UTF-8.
func TestSplitReplyContent_UTF8Safe(t *testing.T) {
	content := strings.Repeat("中文测试", 1000) // ≈12KB of multi-byte runes
	chunks := splitReplyContent(content, 100)
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
		if len(c) > 100 {
			t.Fatalf("chunk %d exceeds cap: %d > 100", i, len(c))
		}
	}
	rejoined := strings.Join(chunks, "")
	if rejoined != content {
		t.Fatalf("UTF-8 round-trip mismatch")
	}
}

// TestAdminStatus_Lark: only the exact configured admin OpenID matches.
func TestAdminStatus_Lark(t *testing.T) {
	fs := &fakeSettings{larkAdmin: "ou_admin"}
	g := newTestGateway(t, fs)

	configured, isAdmin := g.adminStatus(messaging.PlatformLark, "ou_admin")
	if !configured || !isAdmin {
		t.Fatal("expected lark admin to match")
	}
	configured, isAdmin = g.adminStatus(messaging.PlatformLark, "ou_other")
	if !configured || isAdmin {
		t.Fatal("unrelated openid should not match")
	}
	configured, isAdmin = g.adminStatus(messaging.PlatformLark, "")
	if configured || isAdmin {
		t.Fatal("empty sender should not match")
	}
}

// TestAdminStatus_WeChat: any ID on the whitelist matches; whitespace is
// trimmed.
func TestAdminStatus_WeChat(t *testing.T) {
	fs := &fakeSettings{wechatAdminIDs: []string{"@alice_1", "  @bob_2  "}}
	g := newTestGateway(t, fs)

	configured, isAdmin := g.adminStatus(messaging.PlatformWeChat, "@alice_1")
	if !configured || !isAdmin {
		t.Fatal("expected @alice_1 to match")
	}
	configured, isAdmin = g.adminStatus(messaging.PlatformWeChat, "@bob_2")
	if !configured || !isAdmin {
		t.Fatal("expected @bob_2 (after trim) to match")
	}
	configured, isAdmin = g.adminStatus(messaging.PlatformWeChat, "@eve_3")
	if !configured || isAdmin {
		t.Fatal("unlisted sender should not match")
	}
}

// TestAdminStatusConfigured: both platforms report based on their config.
func TestAdminStatusConfigured(t *testing.T) {
	fs := &fakeSettings{}
	g := newTestGateway(t, fs)

	configuredLark, _ := g.adminStatus(messaging.PlatformLark, "x")
	configuredWeChat, _ := g.adminStatus(messaging.PlatformWeChat, "x")
	if configuredLark || configuredWeChat {
		t.Fatal("expected all platforms un-configured initially")
	}

	fs.larkAdmin = "ou_a"
	configuredLark, _ = g.adminStatus(messaging.PlatformLark, "x")
	if !configuredLark {
		t.Fatal("expected lark configured")
	}

	fs.wechatAdminIDs = []string{"@x"}
	configuredWeChat, _ = g.adminStatus(messaging.PlatformWeChat, "x")
	if !configuredWeChat {
		t.Fatal("expected wechat configured")
	}
}

// TestBotSenderID_LarkOnly: botSenderID returns the sophia OpenID for Lark;
// WeChat always empty.
func TestBotSenderID_LarkOnly(t *testing.T) {
	fs := &fakeSettings{larkSophia: "ou_bot"}
	g := newTestGateway(t, fs)

	if got := g.botSenderID(messaging.PlatformLark); got != "ou_bot" {
		t.Fatalf("lark botSenderID: got %q want %q", got, "ou_bot")
	}
	if got := g.botSenderID(messaging.PlatformWeChat); got != "" {
		t.Fatalf("wechat botSenderID should be empty, got %q", got)
	}
}

// TestPendingBuffer_FIFOAndDedup: each sender occupies one slot; exceeding
// the cap evicts the oldest entry.
func TestPendingBuffer_FIFOAndDedup(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})

	// Same sender twice: second call replaces the first entry.
	g.recordPendingContact(&messaging.Message{
		Platform: messaging.PlatformWeChat, SenderID: "@a", MessageID: "1", Content: "first",
	})
	g.recordPendingContact(&messaging.Message{
		Platform: messaging.PlatformWeChat, SenderID: "@a", MessageID: "2", Content: "second",
	})

	list := g.ListPendingContacts(messaging.PlatformWeChat)
	if len(list) != 1 {
		t.Fatalf("dedup: got %d entries want 1", len(list))
	}
	if list[0].MessageID != "2" {
		t.Fatalf("dedup: expected most-recent messageID kept, got %q", list[0].MessageID)
	}

	// Fill past the cap from distinct senders.
	for i := 0; i < maxPendingContacts+5; i++ {
		g.recordPendingContact(&messaging.Message{
			Platform:  messaging.PlatformWeChat,
			SenderID:  "@u" + string(rune('0'+i%10)) + string(rune('a'+i)),
			MessageID: "m",
		})
	}
	list = g.ListPendingContacts(messaging.PlatformWeChat)
	if len(list) > maxPendingContacts {
		t.Fatalf("buffer exceeded cap: got %d entries, want ≤ %d", len(list), maxPendingContacts)
	}
}

// TestPendingBuffer_DropsNonWeChat: Lark messages must not enter the pending
// buffer (the feature is WeChat-specific).
func TestPendingBuffer_DropsNonWeChat(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	g.recordPendingContact(&messaging.Message{
		Platform: messaging.PlatformLark, SenderID: "ou_x", MessageID: "1",
	})
	if got := g.ListPendingContacts(""); len(got) != 0 {
		t.Fatalf("expected non-wechat to be filtered, got %d entries", len(got))
	}
}

// TestRemovePendingContact: removing an entry clears it so it stops showing
// in the UI feed.
func TestRemovePendingContact(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})

	g.recordPendingContact(&messaging.Message{
		Platform: messaging.PlatformWeChat, SenderID: "@a", MessageID: "1",
	})
	g.recordPendingContact(&messaging.Message{
		Platform: messaging.PlatformWeChat, SenderID: "@b", MessageID: "2",
	})

	g.RemovePendingContact(messaging.PlatformWeChat, "@a")

	list := g.ListPendingContacts(messaging.PlatformWeChat)
	if len(list) != 1 || list[0].SenderID != "@b" {
		t.Fatalf("after remove: got %v", list)
	}
}

func TestIMChatDispatcher_IdleExitRemovesDispatcher(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	key := string(messaging.PlatformWeChat) + "|chat-idle"
	d := newIMChatDispatcher(g, key)
	d.running = true
	g.chatDispatches[key] = d

	prev := imChatDispatcherIdleTimeout
	imChatDispatcherIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() { imChatDispatcherIdleTimeout = prev })

	done := make(chan struct{})
	go func() {
		d.loop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not exit after idle timeout")
	}

	g.chatMu.Lock()
	_, ok := g.chatDispatches[key]
	g.chatMu.Unlock()
	if ok {
		t.Fatal("idle dispatcher should remove itself from chatDispatches")
	}
	if d.running {
		t.Fatal("idle dispatcher should clear running flag on exit")
	}
}

// A retired dispatcher must refuse enqueue so a concurrent enqueueChatMessage
// that captured it before the retire cannot resurrect it (which would race two
// dispatchers for the same chat), and removeIdleChatDispatcher must flag it
// retired and drop it from the map. Regression for the retire/enqueue race.
func TestIMChatDispatcher_RetiredEnqueueBounces(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	key := string(messaging.PlatformWeChat) + "|chat-retire-race"

	old := newIMChatDispatcher(g, key)
	g.chatDispatches[key] = old
	if !g.removeIdleChatDispatcher(key, old) {
		t.Fatal("removeIdleChatDispatcher should retire an empty idle dispatcher")
	}
	if !old.retired {
		t.Fatal("retired dispatcher must have retired=true")
	}
	g.chatMu.Lock()
	_, stillMapped := g.chatDispatches[key]
	g.chatMu.Unlock()
	if stillMapped {
		t.Fatal("retired dispatcher must be removed from chatDispatches")
	}

	// A late enqueue against the retired pointer must bounce so the caller
	// re-resolves the live dispatcher instead of resurrecting this one.
	msg := &messaging.Message{Platform: messaging.PlatformWeChat, ChatID: "chat-retire-race", MessageID: "m1"}
	if old.enqueue(context.Background(), msg) {
		t.Fatal("enqueue on a retired dispatcher must return false")
	}

	// A freshly created dispatcher for the same key is live (not retired).
	fresh := newIMChatDispatcher(g, key)
	if fresh.retired {
		t.Fatal("a freshly created dispatcher must not be retired")
	}
}

func TestSweepPendingLogAtEvictsExpiredEntries(t *testing.T) {
	g := newTestGateway(t, &fakeSettings{})
	now := time.Now()
	g.pendingLogAt.Store("stale", now.Add(-pendingLogRetention-time.Second))
	g.pendingLogAt.Store("fresh", now.Add(-pendingLogInterval/2))
	g.pendingLogAt.Store("bad", "not-a-time")
	g.pendingLogSweepAt.Store(0)

	g.sweepPendingLogAt(now)

	if _, ok := g.pendingLogAt.Load("stale"); ok {
		t.Fatal("stale entry should be evicted")
	}
	if _, ok := g.pendingLogAt.Load("bad"); ok {
		t.Fatal("malformed entry should be evicted")
	}
	if _, ok := g.pendingLogAt.Load("fresh"); !ok {
		t.Fatal("fresh entry should be retained")
	}
}
