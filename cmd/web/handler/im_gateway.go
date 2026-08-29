package handler

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/command"
	jobsvc "github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

// imGateway bridges IM platform messages (e.g. Lark, WeChat) to jobs. It
// reuses the same core helpers (createJob / prepareJobMessage) as the web HTTP
// handlers so the IM and Web flows share identical job-creation and send-
// message semantics. Multiple platforms coexist through the repliers map and
// platform-aware dispatch helpers (adminStatus / botSenderID / etc.).
type imGateway struct {
	h             *Handler
	mappingRepo   repository.IMJobMappingRepo
	imMessageRepo repository.IMMessageRepo

	repliersMu sync.RWMutex
	repliers   map[messaging.Platform]messaging.Replier

	queueMu   sync.Mutex
	jobQueues map[string]*imJobQueue

	pendingMu       sync.Mutex
	pendingContacts []pendingContact

	// pendingLogAt throttles the "new contact pending approval" info log to
	// at most one line per minute per (platform, senderID). Without this a
	// chatty stranger (or a spammer) would flood the journal with identical
	// lines; with it, operators still see the first hit promptly but follow-
	// ups collapse into rendering in the Settings panel only.
	pendingLogAt sync.Map // "platform|senderID" → time.Time
	// pendingLogSweepAt throttles full-map sweeps so each new sender does not
	// pay an O(n) cleanup cost. Entries older than pendingLogRetention are
	// dropped opportunistically from logPendingContact.
	pendingLogSweepAt atomic.Int64

	chatMu         sync.Mutex
	chatDispatches map[string]*imChatDispatcher
}

var (
	maxPendingContacts = 20
	// pendingLogInterval bounds how often the same (platform, senderID)
	// can trigger the INFO-level "pending approval" log (doc §5.1).
	pendingLogInterval = time.Minute
	// pendingLogRetention bounds how long rate-limit keys for disappeared /
	// one-off senders stay in memory. Keep it comfortably above the interval so
	// repeated attempts are still coalesced while stale keys eventually age out.
	pendingLogRetention = 10 * time.Minute
	// pendingLogSweepInterval limits how often we iterate pendingLogAt to evict
	// entries older than pendingLogRetention.
	pendingLogSweepInterval = time.Minute

	// imChatReorderWindow buffers a short time to recover correct per-chat
	// ordering when upstream message handlers are scheduled concurrently.
	imChatReorderWindow = 20 * time.Millisecond
	// imChatDispatcherIdleTimeout bounds how long an empty per-chat dispatcher
	// stays resident before it removes itself from g.chatDispatches.
	imChatDispatcherIdleTimeout = 10 * time.Minute
	// queuedJobTotalTimeout caps the total time handleQueuedJobTask will
	// spend retrying a message against a busy job. Without this bound, a job
	// that keeps flipping back into Running can keep the queued-task
	// goroutine alive indefinitely and pin the reply to a stale message.
	queuedJobTotalTimeout = 30 * time.Minute
)

type imChatDispatcher struct {
	g   *imGateway
	key string

	mu      sync.Mutex
	cond    *sync.Cond
	pending imMsgHeap
	running bool
	// stopped is set when the gateway's root ctx is cancelled (process
	// shutdown). loop() observes it after every cond.Wait() and returns,
	// so the goroutine doesn't linger for the idle timeout at shutdown.
	stopped bool
	// retired is set by removeIdleChatDispatcher once this dispatcher has been
	// detached from the gateway's chatDispatches map (idle removal). enqueue
	// checks it under d.mu and refuses to re-arm a retired dispatcher, so a
	// concurrent enqueueChatMessage that captured this pointer before the
	// retire cannot resurrect it (which would race two dispatchers for the
	// same chat and skip root-ctx watcher re-registration). The caller then
	// looks the chat up again under chatMu and gets / creates the live one.
	retired bool
	// stopOnce guards the context.AfterFunc registration so a loop that
	// panics and is relaunched doesn't stack additional watchers.
	stopOnce sync.Once
	// stopRootWatch is the cancel func returned by the context.AfterFunc
	// registration in startLoop. It is called when the dispatcher is retired
	// (idle removal) so the root ctx no longer retains this dispatcher (and the
	// gateway it captures) until process exit — otherwise idle dispatchers for
	// many distinct chats leak for the whole process lifetime.
	stopRootWatch func() bool
	// inflight is the message currently being handled by loop() (set just
	// before HandleMessage, cleared on return). The panic recover path in
	// startLoop reads it so the sender of a poison-pill message still gets
	// an error reply instead of silent drop.
	inflight *imQueuedMsg
}

type imQueuedMsg struct {
	ctx context.Context
	msg *messaging.Message
	ts  int64
}

type imMsgHeap []*imQueuedMsg

func (h imMsgHeap) Len() int { return len(h) }

func (h imMsgHeap) Less(i, j int) bool {
	if h[i].ts != h[j].ts {
		return h[i].ts < h[j].ts
	}
	// Deterministic tie-break.
	return h[i].msg.MessageID < h[j].msg.MessageID
}

func (h imMsgHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *imMsgHeap) Push(x any) {
	*h = append(*h, x.(*imQueuedMsg))
}

func (h *imMsgHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	// Nil the slot before truncating so the popped message can be GC'd
	// even if the underlying array is kept alive by a later Push.
	old[n-1] = nil
	*h = old[:n-1]
	return x
}

// pendingContact records a message from a non-whitelisted sender for admin
// approval (see doc §2.3 / §5.1). Platform + SenderID together dedup — the
// latest message from a given sender replaces any earlier entry.
type pendingContact struct {
	Platform    messaging.Platform `json:"platform"`
	SenderID    string             `json:"sender_id"`
	MessageID   string             `json:"message_id"`
	ContentHint string             `json:"content_hint"`
	ReceivedAt  time.Time          `json:"received_at"`
}

type imJobQueue struct {
	tasks []*imQueuedJobTask
}

type imQueuedJobTask struct {
	msg   *messaging.Message
	req   *model.JobMessageRequest
	jobID string
}

// userError carries a message that is safe to show to external IM users.
// Internal wrapped errors (repo I/O, LLM failures, etc.) never satisfy
// this type, so userReplyText falls back to a generic string for them —
// preventing leaks of file paths, stack traces, or internal IDs.
type userError struct{ msg string }

func (e *userError) Error() string { return e.msg }

func userErrorf(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...)}
}

// userReplyText returns the complete error. Quartet runs in a trusted
// environment and its error contract requires the full failure,
// including wrapped repository and ACP details, to reach the user.
func userReplyText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// newIMGateway constructs an imGateway wired to the given Handler. Repliers
// are registered later via RegisterReplier once each platform's Start* helper
// builds its client — this keeps the gateway agnostic of any single platform.
func newIMGateway(h *Handler, mappingRepo repository.IMJobMappingRepo) *imGateway {
	return &imGateway{
		h:              h,
		mappingRepo:    mappingRepo,
		imMessageRepo:  repository.NewIMMessageRepo(),
		repliers:       make(map[messaging.Platform]messaging.Replier),
		jobQueues:      make(map[string]*imJobQueue),
		chatDispatches: make(map[string]*imChatDispatcher),
	}
}

// RegisterReplier installs the Replier for a given platform. Safe for
// concurrent calls; a later registration overwrites an earlier one.
func (g *imGateway) RegisterReplier(p messaging.Platform, r messaging.Replier) {
	g.repliersMu.Lock()
	defer g.repliersMu.Unlock()
	g.repliers[p] = r
}

// replier returns the Replier registered for p, or nil if none.
func (g *imGateway) replier(p messaging.Platform) messaging.Replier {
	g.repliersMu.RLock()
	defer g.repliersMu.RUnlock()
	return g.repliers[p]
}

var _ messaging.EventHandler = (*imGateway)(nil)

func (g *imGateway) OnMessage(ctx context.Context, msg *messaging.Message) {
	logger.Debugf(ctx, "[im] recv: platform=%s id=%s chat=%s sender=%s type=%s",
		msg.Platform, msg.MessageID, msg.ChatID, msg.SenderID, msg.MessageType)
	g.enqueueChatMessage(ctx, msg)
}

func (g *imGateway) enqueueChatMessage(ctx context.Context, msg *messaging.Message) {
	if msg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := string(msg.Platform) + "|" + msg.ChatID

	// Resolve the live dispatcher and enqueue. enqueue can fail if the
	// dispatcher was retired (idle removal) between our chatMu release and the
	// d.mu acquire inside enqueue — in that race the dispatcher we hold is no
	// longer the chat's owner, so loop again to pick up / create the live one.
	// The loop terminates: a retired dispatcher is always removed from the map
	// (or replaced), so the next lookup yields a different, non-retired one.
	for {
		g.chatMu.Lock()
		d := g.chatDispatches[key]
		if d == nil {
			d = newIMChatDispatcher(g, key)
			g.chatDispatches[key] = d
		}
		g.chatMu.Unlock()

		if d.enqueue(ctx, msg) {
			return
		}
	}
}

func newIMChatDispatcher(g *imGateway, key string) *imChatDispatcher {
	d := &imChatDispatcher{g: g, key: key}
	d.cond = sync.NewCond(&d.mu)
	return d
}

// enqueue adds msg to the dispatcher's pending queue and arms its loop.
// Returns false if the dispatcher has already been retired (detached from the
// gateway map by idle removal) — the caller must re-resolve the live
// dispatcher under chatMu and enqueue there instead of resurrecting this one.
func (d *imChatDispatcher) enqueue(ctx context.Context, msg *messaging.Message) bool {
	if msg == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ts := imMessageSortTime(msg)

	d.mu.Lock()
	if d.retired {
		// Lost the race with removeIdleChatDispatcher: this dispatcher is no
		// longer the chat's owner. Bounce back to the caller to retry.
		d.mu.Unlock()
		return false
	}
	if d.stopped {
		// Process is shutting down; drop the message rather than re-arm
		// the dispatcher and leak a goroutine past shutdown.
		d.mu.Unlock()
		return true
	}
	heap.Push(&d.pending, &imQueuedMsg{ctx: ctx, msg: msg, ts: ts})
	if !d.running {
		d.running = true
		d.startLoop()
	}
	d.cond.Signal()
	d.mu.Unlock()
	return true
}

// startLoop launches the dispatcher goroutine and restarts it if loop()
// exits via panic. Without a restart, a panic leaves d.running=true and
// new enqueues pile up in d.pending with no consumer. Called with d.mu
// held so d.running stays consistent with the goroutine state.
func (d *imChatDispatcher) startLoop() {
	// Wire process-shutdown cancellation once per dispatcher: when the
	// handler's root ctx is cancelled, flip d.stopped and wake the cond
	// so loop() can return instead of idling for imChatDispatcherIdleTimeout.
	// Background()/nil rootCtx (tests) skip the hook; shutdown there is
	// driven by test teardown.
	d.stopOnce.Do(func() {
		if d.g != nil && d.g.h != nil && d.g.h.rootCtx != nil {
			d.stopRootWatch = context.AfterFunc(d.g.h.rootCtx, func() {
				d.mu.Lock()
				d.stopped = true
				d.cond.Broadcast()
				d.mu.Unlock()
			})
		}
	})
	safe.Go(context.Background(), func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf(context.Background(), "[im] dispatcher loop panic: key=%s panic=%v", d.key, r)
				// Re-arm so the next enqueue can start a fresh loop. Any
				// messages already queued will be picked up once a new
				// enqueue triggers startLoop again. Do NOT start a new
				// goroutine from inside the deferred recover — a poison-pill
				// message would cause each restart to panic again and spawn
				// unbounded goroutines. Clear running instead; the next
				// enqueueMessage call will re-launch the loop.
				d.mu.Lock()
				d.running = false
				victim := d.inflight
				d.inflight = nil
				d.mu.Unlock()

				// The panicked message was popped off d.pending before the
				// crash, so without this reply its sender would wait forever.
				// Other queued messages stay in d.pending and resume on the
				// next enqueue.
				if victim != nil && victim.msg != nil {
					d.g.replyToMessage(context.Background(), victim.msg.Platform, victim.msg.MessageID, "处理消息时发生内部错误，请稍后重试")
				}
				return
			}
		}()
		d.loop()
	})
}

func (d *imChatDispatcher) loop() {
	for {
		d.mu.Lock()
		if d.stopped {
			d.running = false
			d.mu.Unlock()
			return
		}
		for d.pending.Len() == 0 {
			idleDeadline := time.Now().Add(imChatDispatcherIdleTimeout)
			timer := time.AfterFunc(imChatDispatcherIdleTimeout, func() {
				d.mu.Lock()
				d.cond.Broadcast()
				d.mu.Unlock()
			})
			d.cond.Wait()
			_ = timer.Stop()
			if d.stopped {
				d.running = false
				d.mu.Unlock()
				return
			}
			if d.pending.Len() == 0 && !time.Now().Before(idleDeadline) {
				d.mu.Unlock()
				if d.g.removeIdleChatDispatcher(d.key, d) {
					return
				}
				d.mu.Lock()
			}
		}
		d.mu.Unlock()

		// Buffer briefly to recover the correct order when multiple message
		// handlers are scheduled concurrently.
		time.Sleep(imChatReorderWindow)

		for {
			d.mu.Lock()
			if d.stopped {
				d.running = false
				d.mu.Unlock()
				return
			}
			if d.pending.Len() == 0 {
				d.mu.Unlock()
				break
			}
			item := heap.Pop(&d.pending).(*imQueuedMsg)
			d.inflight = item
			d.mu.Unlock()

			d.g.HandleMessage(item.ctx, item.msg)

			d.mu.Lock()
			d.inflight = nil
			d.mu.Unlock()
		}
	}
}

func (g *imGateway) removeIdleChatDispatcher(key string, d *imChatDispatcher) bool {
	g.chatMu.Lock()
	defer g.chatMu.Unlock()
	current := g.chatDispatches[key]
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending.Len() != 0 {
		return false
	}
	d.running = false
	// Mark retired so any enqueueChatMessage that captured this pointer before
	// the chatMu acquire above bounces (enqueue returns false) and re-resolves
	// the live dispatcher instead of resurrecting this one.
	d.retired = true
	// This dispatcher is being retired — drop its root-ctx watcher so the root
	// ctx stops retaining it (and the gateway it captures). Safe even if the
	// AfterFunc already fired: stop() is idempotent and returns false then.
	if d.stopRootWatch != nil {
		d.stopRootWatch()
		d.stopRootWatch = nil
	}
	if current == d {
		delete(g.chatDispatches, key)
	}
	return true
}

func parseIMMessageTime(raw string) int64 {
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func imMessageSortTime(msg *messaging.Message) int64 {
	if msg == nil {
		return 0
	}
	if !msg.EventTime.IsZero() {
		return msg.EventTime.UnixMilli()
	}
	ts := parseIMMessageTime(msg.CreateTime)
	if ts != 0 {
		return ts
	}
	ts = parseIMMessageTime(msg.UpdateTime)
	if ts != 0 {
		return ts
	}
	if !msg.ReceivedAt.IsZero() {
		return msg.ReceivedAt.UnixMilli()
	}
	return 0
}

func (g *imGateway) HandleMessage(ctx context.Context, msg *messaging.Message) {
	imMsg := model.NewIMMessage(
		string(msg.Platform),
		msg.MessageID,
		msg.ChatID,
		string(msg.ChatType),
		msg.SenderID,
		msg.MessageType,
		msg.Content,
	)

	if err := g.imMessageRepo.Append(ctx, imMsg); err != nil {
		logger.Errorf(ctx, "[im] persist message failed: id=%s err=%v", msg.MessageID, err)
	}

	g.dispatchMessage(ctx, msg)
}

func (g *imGateway) dispatchMessage(ctx context.Context, msg *messaging.Message) {
	// Capture the server-received timestamp before any dispatch work so the
	// user_input entry lands in the daily file matching "when we got it",
	// even if downstream processing straddles a midnight boundary.
	receivedAt := time.Now()

	if msg.ChatType == messaging.ChatTypeGroup &&
		g.dispatchGroupMessage(msg, g.botSenderID(msg.Platform)) {
		return
	}

	adminConfigured, isAdmin := g.adminStatus(msg.Platform, msg.SenderID)
	if !adminConfigured {
		logger.Debugf(ctx, "[im] drop: no admin configured platform=%s sender=%s", msg.Platform, msg.SenderID)
		return
	}

	if !isAdmin {
		g.logPendingContact(ctx, msg)
		g.recordPendingContact(msg)
		return
	}

	if g.dispatchAdminCommand(msg) {
		return
	}

	// "真实用户输入" 窄口径流：到此处意味着 P2P + admin 配置 + 发送者是 admin +
	// 非命令消息。把这类条目额外写到 user_input/YYYY-MM-DD.jsonl，不影响
	// HandleMessage 里已落的全量 IM 流水（见 docs/feature-2026-05-03-user-input-logging.md §4）。
	// jobId/workspaceId 尽量从已有 IM->Job mapping 里读出来回填；首次建 job 前
	// 的第一条消息这两个字段为空（文档 §3.3 允许）。映射指向的 Job 已被删除时
	// 当作"首次"处理，避免把旧 jobId 写进落盘条目。
	if msg.ChatType == messaging.ChatTypeP2P && g.h.userInputRepo != nil {
		jobID, wsID := g.lookupMappingIDs(ctx, msg)
		input := model.NewIMUserInput(
			receivedAt,
			string(msg.Platform),
			msg.MessageID,
			msg.ChatID,
			msg.SenderID,
			jobID,
			wsID,
			msg.Content,
		)
		if err := g.h.userInputRepo.Append(ctx, input); err != nil {
			logger.Errorf(ctx, "[user_input] append im failed: id=%s err=%v", msg.MessageID, err)
		}
	}

	// routeToJob runs SYNCHRONOUSLY here: dispatchMessage is already called
	// serially per chat (chat dispatcher loop → HandleMessage → dispatchMessage),
	// so resolving the chat→job binding inline keeps that serialization intact.
	// Running it via runAsync would let two messages from the same brand-new
	// chat race through buildQueuedJobTask's "Get mapping → create Job → Save
	// mapping" check-then-act, creating two Jobs and clobbering the mapping. The
	// LLM-blocking part (processJobQueue) is dispatched async inside
	// enqueueRouteToJob, so the chat dispatcher is not held for the model call.
	g.routeToJob(ctx, msg)
}

// lookupMappingIDs returns (jobID, workspaceID) from the existing IM->Job
// mapping for this chat. Both are empty when no mapping exists yet (the
// chat's first message before a job is created), and also when the mapping
// still points to a Job that has already been deleted — otherwise we'd
// stamp the stale jobID onto the user_input entry while the async
// routeToJob path quietly creates a fresh Job for the same message,
// leaving the log inconsistent with the Job that actually processes it.
// Errors are swallowed: the user_input logger is best-effort and must not
// fail the message path.
func (g *imGateway) lookupMappingIDs(ctx context.Context, msg *messaging.Message) (jobID, workspaceID string) {
	if g.mappingRepo == nil || msg == nil {
		return "", ""
	}
	mapping, err := g.mappingRepo.Get(string(msg.Platform), msg.ChatID)
	if err != nil {
		logger.Warnf(ctx, "[user_input] lookup mapping failed: platform=%s chat=%s msg=%s sender=%s err=%v", msg.Platform, msg.ChatID, msg.MessageID, msg.SenderID, err)
		return "", ""
	}
	if mapping == nil {
		return "", ""
	}
	if mapping.JobID != "" {
		if _, ok := g.h.jobService.Get(mapping.JobID); !ok {
			return "", ""
		}
	}
	return mapping.JobID, mapping.WorkspaceID
}

// imJobQueueMaxDepth caps per-job queued messages. A chatty / abusive
// sender whose job is stuck in a long LLM call could otherwise grow
// q.tasks without bound and exhaust memory. When full, new messages are
// rejected with a user-visible "please wait" reply rather than dropped
// silently.
const imJobQueueMaxDepth = 32

func (g *imGateway) enqueueRouteToJob(ctx context.Context, msg *messaging.Message) {
	task, err := g.buildQueuedJobTask(ctx, msg)
	if err != nil {
		logger.Errorf(ctx, "[im] build task failed: msg=%s err=%v", msg.MessageID, err)
		g.replyToMessage(ctx, msg.Platform, msg.MessageID, userReplyText(err))
		return
	}

	g.queueMu.Lock()
	if q, ok := g.jobQueues[task.jobID]; ok {
		if len(q.tasks) >= imJobQueueMaxDepth {
			g.queueMu.Unlock()
			logger.Warnf(ctx, "[im] queue full, dropping message: jobId=%s depth=%d msg=%s", task.jobID, len(q.tasks), msg.MessageID)
			g.replyToMessage(ctx, msg.Platform, msg.MessageID, "系统繁忙，当前任务排队过多，请稍后再试")
			return
		}
		q.tasks = append(q.tasks, task)
		pending := len(q.tasks)
		g.queueMu.Unlock()
		logger.Debugf(ctx, "[im] queued message: jobId=%s pending=%d msg=%s", task.jobID, pending, msg.MessageID)
		return
	}
	g.jobQueues[task.jobID] = &imJobQueue{}
	g.queueMu.Unlock()

	// Draining the queue blocks on the LLM call, so run it async to free the
	// per-chat dispatcher. The chat→job binding above (buildQueuedJobTask) has
	// already run synchronously, so this async hop can no longer race another
	// message into a duplicate Job. processJobQueue owns the queue marker for
	// task.jobID and clears it (via dequeueNextJobTask) when the queue drains.
	g.runAsync(func(ctx context.Context) {
		g.processJobQueue(ctx, task)
	})
}

func (g *imGateway) buildQueuedJobTask(ctx context.Context, msg *messaging.Message) (*imQueuedJobTask, error) {
	_, msgAgent := g.h.settingsService.GetIMConfig()
	if msgAgent == nil || msgAgent.AgentID == "" {
		return nil, userErrorf("未配置 IM 会话 Agent，请在设置中配置")
	}
	resolved, found, err := g.h.agentCatalog.Resolve(ctx, msgAgent.AgentID)
	if err != nil {
		return nil, fmt.Errorf("解析 IM 会话 AgentID %q 失败: %w", msgAgent.AgentID, err)
	}
	if !found {
		return nil, userErrorf("IM 会话 AgentID %q 不存在", msgAgent.AgentID)
	}
	if resolved.Deprecated || resolved.Lifecycle != model.AgentLifecycleActive {
		return nil, userErrorf(
			"IM 会话 AgentID %q 当前不可执行: deprecated=%t lifecycle=%s",
			msgAgent.AgentID,
			resolved.Deprecated,
			resolved.Lifecycle,
		)
	}

	mapping, err := g.mappingRepo.Get(string(msg.Platform), msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("load mapping failed: %w", err)
	}

	j, wsID, err := g.resolveJob(ctx, msg, mapping, msgAgent, resolved.AgentID)
	if err != nil {
		return nil, fmt.Errorf("无法处理消息: %w", err)
	}
	logger.Debugf(ctx, "[im] resolve job: chat=%s jobId=%s agentId=%s revision=%s model=%s mode=%s thought_level=%s",
		msg.ChatID, j.ID, msgAgent.AgentID, resolved.Revision, msgAgent.ModelID, msgAgent.ACPMode, msgAgent.ACPThoughtLevel)

	if err := g.saveJobMapping(ctx, msg, mapping, wsID, j.ID); err != nil {
		logger.Errorf(ctx, "[im] save chat->job mapping failed: chat=%s jobId=%s err=%v", msg.ChatID, j.ID, err)
	}

	return &imQueuedJobTask{
		msg: msg,
		req: &model.JobMessageRequest{
			Messages: []model.RequestMessage{{
				Role:    "user",
				Content: msg.Content,
			}},
			AgentType:       resolved.AgentID,
			ModelID:         msgAgent.ModelID,
			ClientMessageID: imClientMessageID(msg),
			ACPMode:         msgAgent.ACPMode,
			ACPThoughtLevel: msgAgent.ACPThoughtLevel,
		},
		jobID: j.ID,
	}, nil
}

// imClientMessageID namespaces a platform delivery ID by its source chat. IM
// providers guarantee message IDs within their own scope, not across every
// provider/chat Quartet can ingest. Hashing the length-prefixed tuple keeps the
// persisted key compact and avoids separator ambiguity.
func imClientMessageID(msg *messaging.Message) string {
	return imSourceID("message", msg)
}

func imCreateJobClientMessageID(msg *messaging.Message) string {
	return imSourceID("create-job", msg)
}

func imSourceID(kind string, msg *messaging.Message) string {
	if msg == nil || msg.MessageID == "" {
		return ""
	}
	h := sha256.New()
	for _, part := range []string{kind, string(msg.Platform), msg.ChatID, msg.MessageID} {
		fmt.Fprintf(h, "%d:", len(part))
		_, _ = h.Write([]byte(part))
	}
	return "im-" + hex.EncodeToString(h.Sum(nil))
}

func (g *imGateway) saveJobMapping(ctx context.Context, msg *messaging.Message, mapping *repository.IMJobMapping, wsID, jobID string) error {
	if mapping != nil && mapping.JobID == jobID && mapping.WorkspaceID == wsID {
		return nil
	}

	updated := &repository.IMJobMapping{
		Platform:    string(msg.Platform),
		ChatID:      msg.ChatID,
		WorkspaceID: wsID,
		JobID:       jobID,
	}
	if err := g.mappingRepo.Save(updated); err != nil {
		return err
	}
	logger.Infof(ctx, "[im] bind chat=%s -> job=%s", msg.ChatID, jobID)
	return nil
}

func (g *imGateway) processJobQueue(ctx context.Context, current *imQueuedJobTask) {
	jobID := current.jobID
	// Local recover + cleanup: if handleQueuedJobTask (or anything downstream)
	// panics, the queue marker for this jobID would otherwise stay in
	// g.jobQueues forever — enqueueRouteToJob would then keep appending to a
	// queue with no consumer, so messages pile up silently until they hit
	// imJobQueueMaxDepth. The outer safe.Go recovers the panic but does not know
	// about the queue, so we must drop the marker here.
	defer func() {
		if r := recover(); r != nil {
			g.queueMu.Lock()
			delete(g.jobQueues, jobID)
			g.queueMu.Unlock()
			logger.Errorf(ctx, "[im] process queue panic, dropped queue marker: jobId=%s panic=%v", jobID, r)
			panic(r) // let safe.Go log the stack and recover the goroutine
		}
	}()

	for current != nil {
		g.handleQueuedJobTask(ctx, current)
		current = g.dequeueNextJobTask(current.jobID)
	}
}

func (g *imGateway) dequeueNextJobTask(jobID string) *imQueuedJobTask {
	g.queueMu.Lock()
	defer g.queueMu.Unlock()

	q, ok := g.jobQueues[jobID]
	if !ok || len(q.tasks) == 0 {
		delete(g.jobQueues, jobID)
		return nil
	}

	next := q.tasks[0]
	q.tasks = q.tasks[1:]
	return next
}

// handleQueuedJobTask retries sendQueuedJobMessage whenever the target job
// transitions back into the Running state (ErrJobRunning). The total retry
// window is capped at queuedJobTotalTimeout so a perpetually-running job
// cannot pin this goroutine forever — messages that can't be delivered in
// time are surfaced to the user as a timeout error instead.
func (g *imGateway) handleQueuedJobTask(ctx context.Context, task *imQueuedJobTask) {
	taskCtx, cancelTask := context.WithTimeout(ctx, queuedJobTotalTimeout)
	defer cancelTask()
	for {
		if err := g.waitForJobAvailable(taskCtx, task.jobID); err != nil {
			logger.Errorf(ctx, "[im] wait job available failed: jobId=%s err=%v", task.jobID, err)
			g.replyToMessage(ctx, task.msg.Platform, task.msg.MessageID, "等待当前 Job 完成失败: "+userReplyText(err))
			return
		}

		runCtx, cancel := context.WithTimeout(taskCtx, 5*time.Minute)
		err := g.sendQueuedJobMessage(runCtx, task)
		cancel()
		if err == nil {
			return
		}
		if errors.Is(err, jobsvc.ErrJobRunning) {
			if taskCtx.Err() != nil {
				logger.Errorf(ctx, "[im] queued task timed out: jobId=%s msg=%s", task.jobID, task.msg.MessageID)
				g.replyToMessage(ctx, task.msg.Platform, task.msg.MessageID, "消息排队超时，请稍后再试")
				return
			}
			logger.Debugf(ctx, "[im] job busy, waiting: jobId=%s msg=%s", task.jobID, task.msg.MessageID)
			continue
		}

		logger.Errorf(ctx, "[im] send queued message failed: jobId=%s msg=%s err=%v", task.jobID, task.msg.MessageID, err)
		g.replyToMessage(ctx, task.msg.Platform, task.msg.MessageID, userReplyText(err))
		return
	}
}

func (g *imGateway) waitForJobAvailable(ctx context.Context, jobID string) error {
	for {
		j, ok := g.h.jobService.Get(jobID)
		if !ok {
			return userErrorf("job %s 不存在", jobID)
		}
		if j.Status != model.JobStatusRunning {
			return nil
		}

		// Subscribe at the current tail. Passing 0 returns ErrSeqGone on any
		// long-running job whose buffer has GC'd events from prior runs
		// (headSeq>0) — the same guard that maps SSE Last-Event-ID=0 to 410
		// in the HTTP path. We only need to observe future terminal events
		// here, not history, so SnapshotSeq is exactly right.
		//
		// Retry once on ErrSeqGone: between SnapshotSeq() and Subscribe()
		// (separate lock acquisitions) new events may push the gap over the
		// replay limit. A single retry with a fresh SnapshotSeq resolves it.
		var reader *jobsvc.Reader
		for attempt := 0; attempt < 2; attempt++ {
			startSeq := g.h.jobService.SnapshotSeq(jobID)
			var err error
			reader, err = g.h.jobService.Subscribe(jobID, startSeq)
			if err == nil {
				break
			}
			if errors.Is(err, jobsvc.ErrSeqGone) && attempt == 0 {
				logger.Warnf(ctx, "[im_gateway] subscribe race hit, retrying: jobID=%s startSeq=%d err=%v", jobID, startSeq, err)
				continue
			}
			return userErrorf("订阅 job %s 失败: startSeq=%d %v", jobID, startSeq, err)
		}
		j, ok = g.h.jobService.Get(jobID)
		if !ok {
			reader.Close()
			return userErrorf("job %s 不存在", jobID)
		}
		if j.Status != model.JobStatusRunning {
			reader.Close()
			return nil
		}

		waitErr := g.waitForJobTerminal(ctx, reader)
		reader.Close()
		if waitErr != nil {
			return waitErr
		}
	}
}

func (g *imGateway) waitForJobTerminal(ctx context.Context, reader jobEventReader) error {
	for {
		entries, ok := reader.Read(ctx, 16)
		if !ok {
			if err := ctx.Err(); err != nil {
				return err
			}
			return nil
		}
		for _, e := range entries {
			switch e.Event.(type) {
			case *model.JobCompletedEvent, *model.JobFailedEvent, *model.JobStoppedEvent:
				return nil
			}
			if e.Seq > 0 {
				reader.Ack(e.Seq)
			}
		}
	}
}

func (g *imGateway) sendQueuedJobMessage(ctx context.Context, task *imQueuedJobTask) error {
	j, ok := g.h.jobService.Get(task.jobID)
	if !ok {
		return userErrorf("无法处理消息: job %s 不存在", task.jobID)
	}

	runner, opts, err := g.h.prepareJobSend(ctx, j, task.req)
	if err != nil {
		return fmt.Errorf("无法发送消息: %w", err)
	}
	sendStarted := false
	defer func() {
		if !sendStarted {
			if prepared, ok := runner.(jobsvc.PreparedExecutionReleaser); ok {
				prepared.ReleasePreparedExecution()
			}
		}
	}()

	// Subscribe before SendMessage to avoid missing early events. Use
	// SnapshotSeq for the start point — the job is non-Running here, so
	// nothing is publishing, and the new run's first event will land at
	// seq > snapshot. Passing 0 returns ErrSeqGone for any job whose buffer
	// already accumulated GC'd events from prior runs (headSeq>0), which
	// shows up in IM as a permanent failure to deliver replies.
	startSeq := g.h.jobService.SnapshotSeq(j.ID)
	reader, err := g.h.jobService.Subscribe(j.ID, startSeq)
	if err != nil {
		return fmt.Errorf("订阅 job 事件失败: startSeq=%d %w", startSeq, err)
	}
	defer reader.Close()

	result, err := g.h.jobService.SendMessage(ctx, j.ID, runner, opts)
	if err != nil {
		return err
	}
	if !result.Started() {
		return nil
	}
	sendStarted = true

	reply := g.collectReply(ctx, reader)
	if reply == "" {
		reply = "(AI 未生成回复)"
	}

	g.replyToMessage(ctx, task.msg.Platform, task.msg.MessageID, reply)
	return nil
}

func (g *imGateway) dispatchGroupMessage(msg *messaging.Message, botSenderID string) bool {
	if msg.ChatType == messaging.ChatTypeP2P {
		return false
	}
	if botSenderID == "" {
		return true
	}

	for _, mention := range msg.Mentions {
		if mention.ID.OpenID == botSenderID {
			// Break after the first match so a message containing multiple
			// @bot mentions is still dispatched exactly once.
			g.runAsync(func(ctx context.Context) {
				g.handleGroupChat(ctx, msg)
			})
			break
		}
	}
	return true
}

// adminStatus returns whether the platform has an admin configured, and whether
// the given senderID matches the configured admin(s).
func (g *imGateway) adminStatus(platform messaging.Platform, senderID string) (configured bool, isAdmin bool) {
	if g == nil || g.h == nil || g.h.settingsService == nil || senderID == "" {
		return false, false
	}
	trimmed := strings.TrimSpace(senderID)
	if trimmed == "" {
		return false, false
	}

	switch platform {
	case messaging.PlatformLark:
		admin, _ := g.h.settingsService.GetLarkIMSenderIDs()
		admin = strings.TrimSpace(admin)
		if admin == "" {
			return false, false
		}
		return true, admin == trimmed
	case messaging.PlatformWeChat:
		ids := g.h.settingsService.GetWeChatAdminIDs()
		if len(ids) == 0 {
			return false, false
		}
		for _, id := range ids {
			if strings.TrimSpace(id) == trimmed {
				return true, true
			}
		}
		return true, false
	}
	return false, false
}

// botSenderID returns the sender ID used in group-chat @-mention checks. Only
// Lark currently distinguishes a "sophia" bot identity; WeChat P2P does not
// carry @ info in TextItem, so this returns empty for WeChat.
func (g *imGateway) botSenderID(platform messaging.Platform) string {
	if platform != messaging.PlatformLark || g == nil || g.h == nil || g.h.settingsService == nil {
		return ""
	}
	_, sophia := g.h.settingsService.GetLarkIMSenderIDs()
	return strings.TrimSpace(sophia)
}

// logPendingContact emits one INFO line per (platform, senderID) per minute
// for unauthorized senders. Never logs the message content — senders may
// share sensitive data and the whole point of the pending queue is that
// admins approve before content flows anywhere.
func (g *imGateway) logPendingContact(ctx context.Context, msg *messaging.Message) {
	if msg == nil || msg.SenderID == "" {
		return
	}
	key := string(msg.Platform) + "|" + msg.SenderID
	now := time.Now()
	g.sweepPendingLogAt(now)
	if v, ok := g.pendingLogAt.Load(key); ok {
		if t, ok := v.(time.Time); ok && now.Sub(t) < pendingLogInterval {
			return
		}
	}
	g.pendingLogAt.Store(key, now)
	logger.Infof(ctx, "[%s] new contact pending approval: sender=%s content_len=%d",
		msg.Platform, msg.SenderID, len(msg.Content))
}

func (g *imGateway) sweepPendingLogAt(now time.Time) {
	prev := g.pendingLogSweepAt.Load()
	if prev != 0 {
		last := time.Unix(0, prev)
		if now.Sub(last) < pendingLogSweepInterval {
			return
		}
	}
	if !g.pendingLogSweepAt.CompareAndSwap(prev, now.UnixNano()) {
		return
	}
	g.pendingLogAt.Range(func(key, value any) bool {
		t, ok := value.(time.Time)
		if !ok || now.Sub(t) >= pendingLogRetention {
			g.pendingLogAt.Delete(key)
		}
		return true
	})
}

// recordPendingContact enqueues a message from a non-whitelisted sender into
// the pending ring buffer for admin approval (doc §2.3 / §5.1). Currently only
// WeChat populates this — Lark has a different admin model (static OpenID).
// Same platform+senderID dedup: the latest message replaces an earlier entry.
// Buffer is capped at maxPendingContacts (FIFO eviction).
func (g *imGateway) recordPendingContact(msg *messaging.Message) {
	if msg == nil || msg.Platform != messaging.PlatformWeChat || msg.SenderID == "" {
		return
	}

	hint := msg.Content
	// Rate-limit-friendly log: never echo full content.
	const hintMax = 40
	if len([]rune(hint)) > hintMax {
		runes := []rune(hint)
		hint = string(runes[:hintMax]) + "…"
	}

	g.pendingMu.Lock()
	defer g.pendingMu.Unlock()

	// Dedup by (platform, senderID): drop any earlier entry from same sender.
	filtered := g.pendingContacts[:0]
	for _, pc := range g.pendingContacts {
		if pc.Platform == msg.Platform && pc.SenderID == msg.SenderID {
			continue
		}
		filtered = append(filtered, pc)
	}

	filtered = append(filtered, pendingContact{
		Platform:    msg.Platform,
		SenderID:    msg.SenderID,
		MessageID:   msg.MessageID,
		ContentHint: hint,
		ReceivedAt:  time.Now(),
	})

	// FIFO cap — drop oldest entries if we exceed the limit.
	if len(filtered) > maxPendingContacts {
		filtered = filtered[len(filtered)-maxPendingContacts:]
	}
	g.pendingContacts = filtered
}

// ListPendingContacts returns a snapshot of pending-contact entries for the
// given platform. Most-recent first.
func (g *imGateway) ListPendingContacts(platform messaging.Platform) []pendingContact {
	g.pendingMu.Lock()
	defer g.pendingMu.Unlock()

	out := make([]pendingContact, 0, len(g.pendingContacts))
	// Walk in reverse so the newest appears first.
	for i := len(g.pendingContacts) - 1; i >= 0; i-- {
		if platform == "" || g.pendingContacts[i].Platform == platform {
			out = append(out, g.pendingContacts[i])
		}
	}
	return out
}

// RemovePendingContact drops a pending entry by platform + senderID.
func (g *imGateway) RemovePendingContact(platform messaging.Platform, senderID string) {
	g.pendingMu.Lock()
	defer g.pendingMu.Unlock()

	filtered := g.pendingContacts[:0]
	for _, pc := range g.pendingContacts {
		if pc.Platform == platform && pc.SenderID == senderID {
			continue
		}
		filtered = append(filtered, pc)
	}
	g.pendingContacts = filtered
}

func (g *imGateway) dispatchAdminCommand(msg *messaging.Message) bool {
	// Only KNOWN commands are dispatched. Unknown slash text — a typo like
	// /hlep, or a path like /etc/hosts — returns false so the caller falls
	// through to the normal message flow and forwards it to the Agent. This
	// keeps IM aligned with the Web side (command.IsKnown / job_message.go).
	if !command.IsKnown(msg.Content) {
		return false
	}
	cmd, args := command.Parse(msg.Content)

	g.runAsync(func(ctx context.Context) {
		g.handleCommand(ctx, msg, cmd, args)
	})
	return true
}

func (g *imGateway) runAsync(fn func(context.Context)) {
	safe.Go(context.Background(), func() {
		// Inherit shutdown cancellation from the handler's root ctx so IM
		// command handlers don't keep running after the server stops. Fall
		// back to Background() only when the handler wasn't initialized with
		// a root ctx (e.g. in unit tests).
		ctx := context.Background()
		if g.h != nil && g.h.rootCtx != nil {
			ctx = g.h.rootCtx
		}
		fn(ctx)
	})
}

func (g *imGateway) handleCommand(ctx context.Context, msg *messaging.Message, cmd, args string) {
	// Build the platform-agnostic context from the IM mapping. /status and
	// /workspace/list work even without a mapping.
	mapping, err := g.mappingRepo.Get(string(msg.Platform), msg.ChatID)
	if err != nil {
		logger.Errorf(ctx, "[im] handleCommand: get mapping failed: chat=%s err=%v", msg.ChatID, err)
		g.replyToMessage(ctx, msg.Platform, msg.MessageID, "操作失败，请重试。")
		return
	}
	wsID := g.resolveWorkspaceID(mapping)
	jobID := ""
	if mapping != nil {
		jobID = mapping.JobID
	}

	// Preserve legacy IM strict-mode for /new and /job: before the refactor
	// these commands required an explicit `/ws use` (mapping.WorkspaceID != "")
	// and would NOT auto-fall-back to the IM-config / first workspace like
	// regular message routing does. The shared command module, however,
	// receives the resolved wsID unconditionally. For IM we explicitly null
	// out CurrentWorkspaceID on /new and /job when the mapping has no
	// workspace bound, so the command module's "当前未选择工作空间" error path
	// triggers just like before.
	canonical := command.ResolveName(cmd)
	execWsID := wsID
	if canonical == "/new" || canonical == "/job" {
		if mapping == nil || mapping.WorkspaceID == "" {
			execWsID = ""
		} else {
			execWsID = mapping.WorkspaceID
		}
	}

	ec := &command.ExecCtx{
		Ctx:                ctx,
		WorkspaceService:   g.h.workspaceService,
		JobService:         g.h.jobService,
		CurrentWorkspaceID: execWsID,
		CurrentJobID:       jobID,
		Platform:           command.PlatformIM,
	}
	result, ok := command.Execute(cmd, args, ec)
	if !ok {
		g.replyToMessage(ctx, msg.Platform, msg.MessageID, fmt.Sprintf("未知命令: %s\n输入 /help 查看可用命令", cmd))
		return
	}

	// Apply the Action in IM-land: each action type translates to an
	// IMJobMapping write. Text feedback still gets delivered via Replier.
	switch result.Action.Type {
	case command.ActionSwitchWorkspace:
		newMapping := &repository.IMJobMapping{
			Platform:    string(msg.Platform),
			ChatID:      msg.ChatID,
			WorkspaceID: result.Action.WorkspaceID,
			JobID:       "",
		}
		if err := g.mappingRepo.Save(newMapping); err != nil {
			logger.Errorf(ctx, "[im] handleCommand: save mapping failed: chat=%s err=%v", msg.ChatID, err)
			g.replyToMessage(ctx, msg.Platform, msg.MessageID, "切换失败，请重试。")
			return
		}
	case command.ActionBindJob:
		// The action already carries both the job's workspace and its id;
		// copy them verbatim so a cross-workspace bind also updates the ws.
		newMapping := &repository.IMJobMapping{
			Platform:    string(msg.Platform),
			ChatID:      msg.ChatID,
			WorkspaceID: result.Action.WorkspaceID,
			JobID:       result.Action.JobID,
		}
		if err := g.mappingRepo.Save(newMapping); err != nil {
			logger.Errorf(ctx, "[im] handleCommand: save mapping failed: chat=%s job=%s err=%v", msg.ChatID, result.Action.JobID, err)
			g.replyToMessage(ctx, msg.Platform, msg.MessageID, "绑定失败，请重试。")
			return
		}
	case command.ActionNewJob:
		newMapping := &repository.IMJobMapping{
			Platform:    string(msg.Platform),
			ChatID:      msg.ChatID,
			WorkspaceID: result.Action.WorkspaceID,
			JobID:       "",
		}
		if err := g.mappingRepo.Save(newMapping); err != nil {
			logger.Errorf(ctx, "[im] handleCommand: save mapping failed: chat=%s err=%v", msg.ChatID, err)
			g.replyToMessage(ctx, msg.Platform, msg.MessageID, "操作失败，请重试。")
			return
		}
	}

	// /status: the command layer only knows workspace/job IDs — add the
	// IM-specific detail about where the workspace came from (mapping vs
	// settings fallback) to keep IM output unchanged from before.
	reply := result.Message.Text
	if canonical == "/status" && (mapping == nil || mapping.WorkspaceID == "") && wsID != "" {
		reply = appendBeforeJobLine(reply, "工作空间来源: 默认配置")
	}
	if reply != "" {
		g.replyToMessage(ctx, msg.Platform, msg.MessageID, reply)
	}
}

// appendBeforeJobLine inserts a line into a /status reply above the "当前 Job:"
// line so the output order matches the original IM implementation.
func appendBeforeJobLine(text, line string) string {
	idx := strings.Index(text, "当前 Job")
	if idx < 0 {
		if text == "" {
			return line
		}
		return text + "\n" + line
	}
	return text[:idx] + line + "\n" + text[idx:]
}

func (g *imGateway) handleGroupChat(ctx context.Context, msg *messaging.Message) {
	replyAgent := g.h.settingsService.GetGroupReplyAgent()
	if replyAgent == nil || replyAgent.AgentID == "" {
		g.replyToMessage(ctx, msg.Platform, msg.MessageID, "生成回复失败: 未配置群聊回复 Agent，请在设置中配置。")
		return
	}

	prompt, err := g.h.promptService.ResolvePrompt(ctx, consts.KeyGroupChatPrompt)
	if err != nil {
		logger.Errorf(ctx, "[im] group chat: load prompt failed: %v", err)
		g.replyToMessage(ctx, msg.Platform, msg.MessageID, "生成回复失败: 加载群聊 prompt 失败: "+err.Error())
		return
	}
	prompt = renderIMPrompt(prompt, msg)

	messages := []*schema.Message{
		schema.SystemMessage(prompt),
		schema.UserMessage(msg.Content),
	}

	logger.Debugf(ctx, "[im] group chat prompt len=%d", len(prompt))

	reply, err := g.h.generateText(ctx, replyAgent.AgentID, replyAgent.ModelID, replyAgent.ACPThoughtLevel, messages)
	if err != nil {
		logger.Errorf(ctx, "[im] group chat: generate reply failed: chat=%s err=%v", msg.ChatID, err)
		g.replyToMessage(ctx, msg.Platform, msg.MessageID, "生成回复失败: "+err.Error())
		return
	}

	if reply == "" {
		reply = "(AI 未生成回复)"
	}

	g.replyToMessage(ctx, msg.Platform, msg.MessageID, reply)
}

func renderIMPrompt(tpl string, msg *messaging.Message) string {
	// Deterministic replacer: avoids map iteration nondeterminism.
	return strings.NewReplacer(
		"{{Platform}}", string(msg.Platform),
		"{{MessageID}}", msg.MessageID,
		"{{ParentID}}", msg.ParentID,
		"{{RootID}}", msg.RootID,
		"{{ChatID}}", msg.ChatID,
		"{{ChatType}}", string(msg.ChatType),
		"{{SenderID}}", msg.SenderID,
		"{{MessageType}}", msg.MessageType,
		"{{Content}}", msg.Content,
		"{{CreateTime}}", msg.CreateTime,
		"{{UpdateTime}}", msg.UpdateTime,
		"{{Mentions}}", formatIMMentions(msg.Mentions),
		"{{MentionsCount}}", fmt.Sprintf("%d", len(msg.Mentions)),
		"{{RawEvent}}", string(msg.RawEvent),
	).Replace(tpl)
}

func formatIMMentions(mentions []messaging.Mention) string {
	if len(mentions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(mentions))
	for _, m := range mentions {
		// Keep it compact but readable in prompt.
		name := m.Name
		if name == "" {
			name = m.Key
		}
		id := m.ID.OpenID
		if id == "" {
			id = m.ID.UserID
		}
		if id == "" {
			id = m.ID.UnionID
		}
		if id != "" {
			parts = append(parts, fmt.Sprintf("%s(%s)", name, id))
		} else {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ", ")
}

// routeToJob resolves the target Job for the chat and enqueues the message on
// that job's send queue. A queued message is sent only after the job leaves
// the Running state (Completed/Failed/Stopped or any non-running state).
func (g *imGateway) routeToJob(ctx context.Context, msg *messaging.Message) {
	g.enqueueRouteToJob(ctx, msg)
}

// resolveJob returns the live Job bound to this chat, creating a new one via
// the shared createJob helper when no existing job is associated.
func (g *imGateway) resolveJob(
	ctx context.Context,
	msg *messaging.Message,
	mapping *repository.IMJobMapping,
	msgAgent *model.IMSessionAgentConfig,
	agentCommand string,
) (*model.Job, string, error) {
	if mapping != nil && mapping.JobID != "" {
		if j, ok := g.h.jobService.Get(mapping.JobID); ok {
			return j, mapping.WorkspaceID, nil
		}
	}

	wsID := g.resolveWorkspaceID(mapping)
	if wsID == "" {
		return nil, "", fmt.Errorf("未配置 IM 工作空间，请在设置中配置 im_workspace_id 或使用 /workspace use <id>")
	}

	ws, ok := g.h.workspaceService.Get(wsID)
	if !ok {
		return nil, "", fmt.Errorf("工作空间 %s 不存在", wsID)
	}

	req := &model.CreateJobRequest{
		AgentType:       agentCommand,
		ModelID:         msgAgent.ModelID,
		ACPMode:         msgAgent.ACPMode,
		ACPThoughtLevel: msgAgent.ACPThoughtLevel,
		Mode:            model.JobModeInteractive,
		Workdir:         ws.Workdir,
		WorkspaceID:     wsID,
		ClientMessageID: imCreateJobClientMessageID(msg),
	}

	j, _, err := g.h.createJobIdempotent(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("create job failed: %w", err)
	}

	j.Title = consts.DefaultJobTitle
	if err := g.h.jobService.UpdateTitle(j.ID, consts.DefaultJobTitle); err != nil {
		logger.Errorf(ctx, "[im] update job title failed: jobId=%s err=%v", j.ID, err)
	}

	logger.Debugf(ctx, "[im] create job=%s for chat=%s", j.ID, msg.ChatID)
	return j, wsID, nil
}

func (g *imGateway) resolveWorkspaceID(mapping *repository.IMJobMapping) string {
	if mapping != nil && mapping.WorkspaceID != "" {
		return mapping.WorkspaceID
	}
	cfgWsID, _ := g.h.settingsService.GetIMConfig()
	if cfgWsID != "" {
		return cfgWsID
	}
	wsList := g.h.workspaceService.List()
	if len(wsList) > 0 {
		return wsList[0].ID
	}
	return ""
}

func (g *imGateway) collectReply(ctx context.Context, reader jobEventReader) string {
	var sb strings.Builder
	for {
		entries, ok := reader.Read(ctx, 32)
		if !ok {
			if ctx.Err() != nil && sb.Len() == 0 {
				return "处理超时，请重试。"
			}
			return sb.String()
		}
		for _, entry := range entries {
			switch e := entry.Event.(type) {
			case *model.TextMessageContentEvent:
				if e.Role == model.MessageRoleAssistant {
					if ext, ok := e.External["isThinking"]; ok {
						if thinking, ok := ext.(bool); ok && thinking {
							if entry.Seq > 0 {
								reader.Ack(entry.Seq)
							}
							continue
						}
					}
					sb.WriteString(e.Delta)
				}
			case *model.RunFinishedEvent:
				if entry.Seq > 0 {
					reader.Ack(entry.Seq)
				}
				return sb.String()
			case *model.RunErrorEvent:
				if entry.Seq > 0 {
					reader.Ack(entry.Seq)
				}
				if sb.Len() > 0 {
					return sb.String()
				}
				return fmt.Sprintf("执行出错: %s", e.Message)
			case *model.JobCompletedEvent, *model.JobFailedEvent, *model.JobStoppedEvent:
				if entry.Seq > 0 {
					reader.Ack(entry.Seq)
				}
				return sb.String()
			}
			if entry.Seq > 0 {
				reader.Ack(entry.Seq)
			}
		}
	}
}

// jobEventReader is the narrow interface IM gateway needs from a buffer
// reader: blocking Read + Ack. Decoupled from the concrete *bufferReader
// only so the gateway code stays mockable in tests.
type jobEventReader interface {
	Read(ctx context.Context, maxN int) ([]jobsvc.ReadEntry, bool)
	Ack(seq uint64)
	Close()
}

func (g *imGateway) replyToMessage(ctx context.Context, platform messaging.Platform, messageID, content string) {
	r := g.replier(platform)
	if r == nil {
		logger.Debugf(ctx, "[im] no replier configured for platform=%s; would reply: %s", platform, content)
		return
	}
	if platform == messaging.PlatformWeChat {
		if mr, ok := r.(messaging.MediaReplier); ok && g.replyMediaAware(ctx, mr, platform, messageID, content) {
			return
		}
	}
	g.replyTextChunks(ctx, r, platform, messageID, content)
}

func (g *imGateway) replyTextChunks(ctx context.Context, r messaging.Replier, platform messaging.Platform, messageID, content string) {
	// Split long replies so we don't hit platform content-size limits. Short
	// replies take the fast path with a single element slice.
	chunks := splitReplyContent(content, maxReplyChunkBytesForPlatform(platform))
	for i, chunk := range chunks {
		if err := r.ReplyText(ctx, messageID, chunk); err != nil {
			logger.Errorf(ctx, "[im] reply failed: platform=%s msg=%s chunk=%d/%d err=%v",
				platform, messageID, i+1, len(chunks), err)
			return
		}
	}
}

type mediaReplyPart struct {
	text     string
	media    messaging.MediaPayload
	fallback string
}

var reReplyMarkdownMedia = regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

func (g *imGateway) replyMediaAware(ctx context.Context, r messaging.MediaReplier, platform messaging.Platform, messageID, content string) bool {
	parts, hasMedia := splitMediaReplyParts(content)
	if !hasMedia {
		return false
	}
	for _, part := range parts {
		if part.text != "" {
			g.replyTextChunks(ctx, r, platform, messageID, part.text)
			continue
		}
		if err := r.ReplyMedia(ctx, messageID, part.media); err != nil {
			logger.Errorf(ctx, "[im] media reply failed: platform=%s msg=%s fallback=%q err=%v",
				platform, messageID, part.fallback, err)
			g.replyTextChunks(ctx, r, platform, messageID, part.fallback)
		}
	}
	return true
}

func splitMediaReplyParts(content string) ([]mediaReplyPart, bool) {
	matches := reReplyMarkdownMedia.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return []mediaReplyPart{{text: content}}, false
	}

	parts := make([]mediaReplyPart, 0, len(matches)*2+1)
	last := 0
	hasMedia := false
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		start, end := m[0], m[1]
		if start > last {
			parts = appendTextReplyPart(parts, content[last:start])
		}
		target := strings.TrimSpace(content[m[2]:m[3]])
		payload, ok := mediaPayloadFromReplyTarget(target)
		if ok {
			hasMedia = true
			parts = append(parts, mediaReplyPart{media: payload, fallback: content[start:end]})
		} else {
			parts = appendTextReplyPart(parts, content[start:end])
		}
		last = end
	}
	if last < len(content) {
		parts = appendTextReplyPart(parts, content[last:])
	}
	return parts, hasMedia
}

func appendTextReplyPart(parts []mediaReplyPart, text string) []mediaReplyPart {
	if text == "" {
		return parts
	}
	if len(parts) > 0 && parts[len(parts)-1].text != "" {
		parts[len(parts)-1].text += text
		return parts
	}
	return append(parts, mediaReplyPart{text: text})
}

func mediaPayloadFromReplyTarget(target string) (messaging.MediaPayload, bool) {
	if target == "" || target == "#" {
		return messaging.MediaPayload{}, false
	}
	lower := strings.ToLower(target)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return messaging.MediaPayload{URL: target}, true
	}
	if filepath.IsAbs(target) {
		path := filepath.Clean(target)
		if !isPathInAllowedRegion(path) {
			return messaging.MediaPayload{}, false
		}
		return messaging.MediaPayload{Path: path}, true
	}
	return messaging.MediaPayload{}, false
}

const (
	// maxLarkReplyChunkBytes caps a single outbound Lark post chunk. 8000 bytes
	// leaves generous headroom under Lark's post content limit while still
	// batching long agent replies into a small number of messages.
	maxLarkReplyChunkBytes = 8000
	// maxWeChatReplyChunkBytes is lower because WeChat/iLink text messages have
	// a smaller practical limit than Lark post content. Keep margin for JSON and
	// upstream boundary differences.
	maxWeChatReplyChunkBytes = 3500
)

func maxReplyChunkBytesForPlatform(platform messaging.Platform) int {
	switch platform {
	case messaging.PlatformWeChat:
		return maxWeChatReplyChunkBytes
	case messaging.PlatformLark:
		return maxLarkReplyChunkBytes
	default:
		return maxLarkReplyChunkBytes
	}
}

// splitReplyContent breaks content into UTF-8 safe chunks of at most
// maxBytes bytes each. Prefers a newline break in the last quarter of a
// chunk so paragraphs stay intact across splits. Content that already fits
// is returned as a single-element slice so the common case avoids copying.
func splitReplyContent(content string, maxBytes int) []string {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return []string{content}
	}

	var chunks []string
	for len(content) > maxBytes {
		splitAt := maxBytes
		// Back off to a UTF-8 rune boundary so we never cut a multi-byte
		// character (Chinese, emoji).
		for splitAt > 0 && !utf8.RuneStart(content[splitAt]) {
			splitAt--
		}
		// Prefer a newline break within the last quarter of the chunk so
		// visual splits align with paragraph boundaries when possible.
		if idx := strings.LastIndex(content[:splitAt], "\n"); idx > maxBytes*3/4 {
			splitAt = idx + 1
		}
		if splitAt == 0 {
			// Degenerate input (single rune wider than maxBytes). Emit the
			// whole rune so we make forward progress.
			_, size := utf8.DecodeRuneInString(content)
			splitAt = size
		}
		chunks = append(chunks, content[:splitAt])
		content = content[splitAt:]
	}
	if content != "" {
		chunks = append(chunks, content)
	}
	return chunks
}
