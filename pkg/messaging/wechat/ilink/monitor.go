package ilink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	deeppath "github.com/fanlv/quartet/types/path"
)

const (
	maxConsecutiveFailures = 5
	initialBackoff         = 3 * time.Second
	maxBackoff             = 60 * time.Second
	sessionExpiredBackoff  = 5 * time.Second
	// maxInflightHandlers caps the number of concurrent handler goroutines
	// spawned per Monitor. A slow or stuck handler (LLM call, CDN download)
	// would otherwise let every long-poll batch pile up new goroutines
	// without bound.
	maxInflightHandlers = 32
	// handlerGracePeriod bounds how long a handler goroutine may keep running
	// after Run()'s parent ctx is cancelled. Needs to cover the worst-case
	// synchronous CDN download (cdnDownloadTimeout is 20s) plus the downstream
	// OnMessage call so a restart/shutdown mid-download doesn't silently drop
	// the message — sync buf is already persisted (commit-before-process) so
	// iLink will NOT replay it on reconnect.
	handlerGracePeriod = 60 * time.Second
)

// MessageHandler is called for each received WeixinMessage.
type MessageHandler func(ctx context.Context, client *Client, msg WeixinMessage)

// Monitor manages the long-poll loop for receiving messages.
type Monitor struct {
	client        *Client
	handler       MessageHandler
	getUpdatesBuf string
	bufPath       string
	failures      int
	lastActivity  time.Time

	// sem bounds the number of concurrent handler goroutines. Prevents
	// unbounded goroutine growth when the handler is slower than incoming
	// throughput (stuck CDN download, LLM call, etc.).
	sem chan struct{}

	// wg tracks inflight handler goroutines so Run() can drain them on
	// shutdown. Without this, a parent-ctx cancel would leave media
	// downloads (CDN decrypt, file write) half-finished even though the
	// sync buf / seenMsgs have already been committed for those messages,
	// producing silent message loss across Restart() / process shutdown.
	wg sync.WaitGroup

	// expired is set when iLink returns ErrCodeSessionExpired with an
	// already-empty sync buf — i.e. the bot_token itself is dead and can't
	// be recovered by a local buf reset. Exposed via IsExpired() so upstream
	// (Manager / Web UI) can surface "please re-scan".
	expired atomic.Bool
}

// NewMonitor creates a new long-poll monitor and loads any persisted
// get_updates_buf so reconnects resume from the last seen seq.
func NewMonitor(client *Client, handler MessageHandler) (*Monitor, error) {
	accountID := normalizeAccountID(client.BotID())
	bufPath := deeppath.WeChatSyncBufFile(accountID)

	m := &Monitor{
		client:       client,
		handler:      handler,
		bufPath:      bufPath,
		lastActivity: time.Now(),
		sem:          make(chan struct{}, maxInflightHandlers),
	}
	m.loadBuf()
	return m, nil
}

// Run starts the long-poll loop. It blocks until ctx is cancelled. Transient
// failures trigger exponential backoff; session-expired (errcode=-14) resets
// the sync buf and reconnects after a short sleep.
//
// On ctx cancellation Run() drains inflight handler goroutines (up to
// handlerGracePeriod) before returning. This matters because sync buf and
// seenMsgs are advanced synchronously BEFORE the handler runs — if Run()
// returned immediately, a handler mid-CDN-download would be ctx-cancelled,
// produce a placeholder, and the message would be silently lost on iLink's
// side (no replay) and the dedup cache's side (seen until TTL / restart).
func (m *Monitor) Run(ctx context.Context) error {
	logger.Info("[wechat/monitor] starting long-poll loop: bot=%s", m.client.BotID())

	defer m.drainHandlers()

	for {
		select {
		case <-ctx.Done():
			logger.Info("[wechat/monitor] shutting down: bot=%s", m.client.BotID())
			return ctx.Err()
		default:
		}

		resp, err := m.client.GetUpdates(ctx, m.getUpdatesBuf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.failures++
			backoff := m.calcBackoff()
			logger.Warn("[wechat/monitor] GetUpdates error (%d/%d, backoff=%s): %v",
				m.failures, maxConsecutiveFailures, backoff, err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// Reset failure counter on any successful response.
		m.failures = 0
		m.lastActivity = time.Now()

		// Session expired — two sub-cases:
		//   1. sync buf is non-empty → local buf is stale, a reset usually
		//      clears the error. Reconnect silently.
		//   2. sync buf already empty → bot_token itself has expired; no
		//      local fix is possible and the user must re-scan. Log a loud
		//      warning + set the expired flag so upstream can surface the
		//      state to the UI. We still keep polling (best-effort: if the
		//      server recovers, we resume) but expired stays latched.
		if resp.ErrCode == ErrCodeSessionExpired {
			if m.getUpdatesBuf != "" {
				logger.Warn("[wechat/monitor] session expired, resetting sync buf: bot=%s", m.client.BotID())
				m.getUpdatesBuf = ""
				m.saveBuf()
			} else if !m.expired.Load() {
				logger.Warn("[wechat/monitor] bot_token expired, re-login required: bot=%s", m.client.BotID())
				m.expired.Store(true)
			}
			select {
			case <-time.After(sessionExpiredBackoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// Clear the expired flag once we receive a clean response again
		// (e.g. server temporarily 5xx'd, then recovered).
		if m.expired.Load() {
			m.expired.Store(false)
		}

		// Other server errors — log but keep polling.
		if resp.Ret != 0 && resp.ErrCode != 0 {
			logger.Warn("[wechat/monitor] server error: ret=%d errcode=%d errmsg=%s",
				resp.Ret, resp.ErrCode, resp.ErrMsg)
			continue
		}

		// Update buf for next poll.
		if resp.GetUpdatesBuf != "" {
			m.getUpdatesBuf = resp.GetUpdatesBuf
			m.saveBuf()
		}

		// Group messages by sender, then dispatch one goroutine per sender
		// so each sender's messages are processed in the same order iLink
		// delivered them. Different senders still run concurrently so the
		// poll loop isn't blocked — the getupdates call holds the server's
		// attention for up to 35s and piled-up handlers lose that window.
		// Concurrency is bounded by `sem`; a recover keeps one bad message
		// from taking down the whole monitor.
		//
		// Handler goroutines get a detached context (not the poll loop's
		// ctx) so a Manager.Restart() or process shutdown doesn't cancel
		// CDN downloads mid-flight. The sync buf and seenMsgs have
		// already been advanced for this message — if we let the download
		// fail the user sees a "[图片]" placeholder and iLink never
		// replays. Run()'s defer drainHandlers() caps the grace period.
		bySender := make(map[string][]WeixinMessage, len(resp.Msgs))
		senderOrder := make([]string, 0)
		for _, msg := range resp.Msgs {
			if _, ok := bySender[msg.FromUserID]; !ok {
				senderOrder = append(senderOrder, msg.FromUserID)
			}
			bySender[msg.FromUserID] = append(bySender[msg.FromUserID], msg)
		}
		for _, sender := range senderOrder {
			msgs := bySender[sender]
			select {
			case m.sem <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			m.wg.Add(1)
			go func(msgs []WeixinMessage) {
				defer m.wg.Done()
				defer func() {
					<-m.sem
				}()
				// Each message gets its own handlerGracePeriod timeout.
				// A per-batch budget (what we used to do) silently dropped
				// every remaining message once the first slow one burned
				// the whole window — sync buf and seenMsgs were already
				// advanced for these, so iLink never replays them and the
				// loss was permanent. Running each message under a fresh
				// ctx costs at most len(msgs)*grace in total, bounded by
				// drainHandlers on shutdown.
				for _, msg := range msgs {
					// recover per-message so a single panic skips just
					// that message instead of abandoning the rest of the
					// sender's ordered batch.
					func(msg WeixinMessage) {
						msgCtx, cancel := context.WithTimeout(context.Background(), handlerGracePeriod)
						defer cancel()
						defer func() {
							if r := recover(); r != nil {
								logger.Error("[wechat/monitor] handler panic: msg=%d panic=%v\n%s", msg.MessageID, r, debug.Stack())
							}
						}()
						m.handler(msgCtx, m.client, msg)
					}(msg)
				}
			}(msgs)
		}
	}
}

// calcBackoff returns an exponential backoff duration capped at maxBackoff.
func (m *Monitor) calcBackoff() time.Duration {
	d := initialBackoff
	for i := 1; i < m.failures; i++ {
		d *= 2
		if d > maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// drainHandlers waits for all inflight handler goroutines to finish, bounded
// by handlerGracePeriod. Called from Run()'s defer so shutdown/Restart waits
// for CDN downloads instead of abandoning them. Each handler already has its
// own handlerGracePeriod timeout so this Wait is guaranteed to return.
//
// On timeout we fall through without cancelling the waiter goroutine. That
// waiter stays blocked on m.wg.Wait() until the laggard handlers exit —
// which they will, bounded by their own handlerGracePeriod-scoped ctx.
// The cost is a single goroutine living a few extra seconds; a handler
// that ignores its ctx would leak this goroutine, but that would also mean
// every Monitor.Restart from here on is paying for the same handler.
func (m *Monitor) drainHandlers() {
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(handlerGracePeriod + 5*time.Second):
		logger.Warn("[wechat/monitor] drain timeout, some handlers still running: bot=%s", m.client.BotID())
	}
}

// IsExpired reports whether the bot_token has been observed to be
// unrecoverable (iLink returned errcode=-14 with an already-empty sync buf).
// Flipped back to false once the server returns a clean response again.
func (m *Monitor) IsExpired() bool {
	return m.expired.Load()
}

type syncData struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

func (m *Monitor) loadBuf() {
	sb := fileserver.GetFileManager()
	result, err := sb.FileRead(&fsmodel.FileReadRequest{File: m.bufPath})
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("[wechat/monitor] failed to load sync buf: bot=%s path=%s err=%v", m.client.BotID(), m.bufPath, err)
		}
		return
	}
	var s syncData
	if err := json.Unmarshal([]byte(result.Content), &s); err != nil {
		logger.Warn("[wechat/monitor] failed to decode sync buf: bot=%s path=%s bytes=%d err=%v", m.client.BotID(), m.bufPath, len(result.Content), err)
		return
	}
	if s.GetUpdatesBuf != "" {
		m.getUpdatesBuf = s.GetUpdatesBuf
		logger.Debug("[wechat/monitor] loaded sync buf from %s", m.bufPath)
	}
}

// saveBuf persists the current sync buf via tmp file + rename so a mid-write
// crash cannot leave a truncated JSON that loadBuf would then silently drop,
// resetting getUpdatesBuf to "" and causing iLink to replay the last batch of
// messages. The in-memory seenMsgs dedup is lost on restart, so replayed
// messages would otherwise be re-dispatched to the handler. Mirrors the
// tmp+rename pattern used by auth.go:SaveCredentials in the same package.
func (m *Monitor) saveBuf() {
	dir := filepath.Dir(m.bufPath)
	sb := fileserver.GetFileManager()
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		logger.Warn("[wechat/monitor] failed to create buf dir: %v", err)
		return
	}
	data, _ := json.Marshal(syncData{GetUpdatesBuf: m.getUpdatesBuf})
	if err := sb.FileWrite(&fsmodel.FileWriteRequest{
		File:    m.bufPath,
		Content: string(data),
		Mode:    0o600,
		Atomic:  true,
	}); err != nil {
		logger.Warn("[wechat/monitor] failed to persist buf: %v", err)
	}
}

// FormatMessageSummary returns a short description of a message for logging.
func FormatMessageSummary(msg WeixinMessage) string {
	text := ""
	for _, item := range msg.ItemList {
		if item.Type == ItemTypeText && item.TextItem != nil {
			text = item.TextItem.Text
			break
		}
	}
	// Truncate on rune boundaries so Chinese text doesn't render as "\xe4\xb8"
	// when a multi-byte character lands on the 50-byte mark.
	if r := []rune(text); len(r) > 50 {
		text = string(r[:50]) + "..."
	}
	return fmt.Sprintf("from=%s type=%d state=%d text=%q",
		msg.FromUserID, msg.MessageType, msg.MessageState, text)
}
