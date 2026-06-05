package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	envAppID     = "LARKSUITE_CLI_APP_ID"
	envAppSecret = "LARKSUITE_CLI_APP_SECRET"
	envBrand     = "LARKSUITE_CLI_BRAND"
)

const (
	imMessageReceiveEventType  = "im.message.receive_v1"
	imMessageReadEventType     = "im.message.message_read_v1"
	imMessageReactionCreatedV1 = "im.message.reaction.created_v1"
	imMessageReactionDeletedV1 = "im.message.reaction.deleted_v1"
)

// ignoredEventTypes are events the open-platform app is subscribed to but we
// deliberately do not process. Without explicit handlers the SDK logs each
// one as "not found handler" (surfaced by sdkLogger.Error → WARN), which
// drowns out real warnings. Register a noop per type instead of relying on
// the error-path downgrade.
var ignoredEventTypes = []string{
	imMessageReadEventType,
	imMessageReactionCreatedV1,
	imMessageReactionDeletedV1,
}

type rawEvent struct {
	Schema string                `json:"schema"`
	Header larkevent.EventHeader `json:"header"`
	Event  json.RawMessage       `json:"event"`
}

type imMessageEvent struct {
	Message struct {
		MessageID   string       `json:"message_id"`
		ParentID    string       `json:"parent_id"`
		RootID      string       `json:"root_id"`
		ChatID      string       `json:"chat_id"`
		ChatType    string       `json:"chat_type"`
		MessageType string       `json:"message_type"`
		Content     string       `json:"content"`
		CreateTime  string       `json:"create_time"`
		UpdateTime  string       `json:"update_time"`
		Mentions    []rawMention `json:"mentions"`
	} `json:"message"`
	Sender struct {
		SenderID struct {
			OpenID string `json:"open_id"`
		} `json:"sender_id"`
		SenderType string `json:"sender_type"`
	} `json:"sender"`
}

// sdkLogger bridges the Lark SDK's Logger interface into pkg/logger. The
// optional listener back-pointer lets the Error path consult Listener.stopped
// so a shutdown-driven WebSocket close (which the SDK reports as "receive
// message failed: ... use of closed network connection") is demoted to Debug
// instead of WARN — without it every restart leaves a misleading WARN line
// even though the operator just stopped the process intentionally.
type sdkLogger struct {
	listener *Listener
}

func (l *sdkLogger) Debug(_ context.Context, _ ...interface{}) {}

// Info is kept at the logger.Info level (not demoted to Debug) so operators
// can see the SDK's reconnect lifecycle — "connected to", "trying to
// reconnect", "disconnected to" are emitted at Info by the SDK and are the
// only signal we have that the WebSocket recovered after a network blip.
func (l *sdkLogger) Info(_ context.Context, args ...interface{}) {
	logger.Info("[lark/sdk] %v", redactSensitiveParams(fmt.Sprint(args...)))
}
func (l *sdkLogger) Warn(_ context.Context, args ...interface{}) {
	logger.Warn("[lark/sdk] %v", redactSensitiveParams(fmt.Sprint(args...)))
}
func (l *sdkLogger) Error(_ context.Context, args ...interface{}) {
	msg := redactSensitiveParams(fmt.Sprint(args...))
	if isUnsupportedEventLog(msg) {
		logger.Warn("[lark/sdk] %v", msg)
		return
	}
	// The SDK funnels any non-fatal read/close fault on the WebSocket through
	// its Error logger — including normal lifecycle events (peer-initiated
	// close 1006/1001, EOF after close, "use of closed network connection"
	// during shutdown). Our ws_runtime.go owns reconnection, so these surface
	// as a misleading ERROR followed within a second by the real INFO/WARN
	// reconnect line. Downgrade them to WARN; genuine protocol/auth/parse
	// failures keep ERROR. When the listener has been explicitly Stop()'d
	// (process shutdown / Restart) the close is fully expected, so demote
	// further to Debug to keep the shutdown log clean.
	if isRecoverableWSDisconnectLog(msg) {
		if l.listener != nil && l.listener.stopped.Load() {
			logger.Debug("[lark/sdk] %v", msg)
			return
		}
		logger.Warn("[lark/sdk] %v", msg)
		return
	}
	logger.Error("[lark/sdk] %v", msg)
}

var _ larkcore.Logger = (*sdkLogger)(nil)
var _ messaging.Listener = (*Listener)(nil)

type ConfigProvider func() (appID, appSecret string)

type Listener struct {
	configProvider ConfigProvider
	brand          string
	handler        messaging.EventHandler
	client         *larkws.Client
	// stopped is set by Stop() so late-arriving in-flight events are dropped
	// instead of being dispatched after Manager.Restart().
	stopped atomic.Bool
	// imageDownloader allows overriding download behavior (e.g. for tests).
	imageDownloader func(ctx context.Context, messageID, imageKey string) (string, error)

	// Cache the OpenAPI client so image downloads across a post share the
	// SDK's tenant_access_token cache. Rebuilding lark.Client for every
	// image in a multi-image post wastes token requests.
	openClient larkClientCache
}

// NewListener constructs a Listener. configProvider is required — callers
// must supply a function that returns (app_id, app_secret) at dispatch time
// so credential rotation doesn't require re-instantiating the listener at
// all (see Manager.startLocked). Tests that only exercise decoding may
// instantiate Listener directly without going through NewListener.
func NewListener(handler messaging.EventHandler, configProvider ConfigProvider) *Listener {
	l := &Listener{
		brand:          os.Getenv(envBrand),
		handler:        handler,
		configProvider: configProvider,
	}
	return l
}

// Stop marks the listener as stopped so any in-flight or late-arriving events
// are dropped instead of being re-dispatched to the handler.
func (l *Listener) Stop() {
	l.stopped.Store(true)
}

func (l *Listener) Start(ctx context.Context) error {
	appID, appSecret := l.configProvider()
	if appID == "" || appSecret == "" {
		return fmt.Errorf("lark app_id and app_secret are required (set via settings page)")
	}

	ed := dispatcher.NewEventDispatcher("", "")
	ed.InitConfig(larkevent.WithLogger(&sdkLogger{listener: l}))

	rawHandler := func(ctx context.Context, event *larkevent.EventReq) error {
		if l.stopped.Load() {
			return nil
		}
		if event.Body == nil {
			return nil
		}
		var raw rawEvent
		if err := json.Unmarshal(event.Body, &raw); err != nil {
			// Log a truncated preview so we can debug schema drift without
			// spilling the entire event body (may contain user content or
			// tokens) into logs.
			logger.Error("[lark] parse event failed: %v body_len=%d preview=%s",
				err, len(event.Body), truncateForLog(event.Body, 256))
			return nil
		}
		logger.Debug("[lark] event received: type=%s event_id=%s",
			raw.Header.EventType, raw.Header.EventID)
		l.handleIMMessage(ctx, &raw)
		return nil
	}

	ed.OnCustomizedEvent(imMessageReceiveEventType, rawHandler)

	// Register a no-op handler for events we don't process to avoid SDK
	// "not found handler" error logs. Demoted to Debug — once registered
	// the arrival is uninteresting.
	for _, evType := range ignoredEventTypes {
		ed.OnCustomizedEvent(evType, func(ctx context.Context, event *larkevent.EventReq) error {
			if l.stopped.Load() {
				return nil
			}
			logger.Debug("[lark] ignored event: type=%s", evType)
			return nil
		})
	}

	l.client = larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(ed),
		larkws.WithDomain(resolveOpenDomain(l.brand)),
		larkws.WithLogger(&sdkLogger{listener: l}),
		larkws.WithAutoReconnect(false),
	)

	logger.Info("[lark] connecting WebSocket: appId=%s", appID)

	return l.startWebSocket(ctx)
}

func (l *Listener) handleIMMessage(ctx context.Context, raw *rawEvent) {
	var ev imMessageEvent
	if err := json.Unmarshal(raw.Event, &ev); err != nil {
		logger.Error("[lark] parse IM message event failed: %v", err)
		return
	}

	receivedAt := time.Now()
	content := l.decodeMessageContent(ctx, ev.Message.MessageID, ev.Message.MessageType, ev.Message.Content, ev.Message.Mentions)

	msg := &messaging.Message{
		Platform:    messaging.PlatformLark,
		MessageID:   ev.Message.MessageID,
		ParentID:    ev.Message.ParentID,
		RootID:      ev.Message.RootID,
		ChatID:      ev.Message.ChatID,
		ChatType:    messaging.ChatType(ev.Message.ChatType),
		SenderID:    ev.Sender.SenderID.OpenID,
		MessageType: ev.Message.MessageType,
		Content:     content,
		CreateTime:  ev.Message.CreateTime,
		UpdateTime:  ev.Message.UpdateTime,
		ReceivedAt:  receivedAt,
		EventTime:   parseLarkMillis(ev.Message.CreateTime),
		Mentions:    toMentions(ev.Message.Mentions),
		RawEvent:    raw.Event,
	}
	if msg.EventTime.IsZero() {
		msg.EventTime = parseLarkMillis(ev.Message.UpdateTime)
	}

	l.handler.OnMessage(ctx, msg)
}

// newLarkClient constructs an OpenAPI client for the given brand. Shared by
// Listener (for resource downloads) and Replier (for sending messages).
func newLarkClient(appID, appSecret, brand string) (*lark.Client, error) {
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("lark credentials not configured")
	}
	return lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(resolveOpenDomain(brand))), nil
}

type larkClientCache struct {
	mu     sync.Mutex
	client *lark.Client
	id     string
	secret string
}

func (c *larkClientCache) Get(configFn ConfigProvider, brand string) (*lark.Client, error) {
	appID, appSecret := configFn()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil && c.id == appID && c.secret == appSecret {
		return c.client, nil
	}
	client, err := newLarkClient(appID, appSecret, brand)
	if err != nil {
		return nil, err
	}
	c.client, c.id, c.secret = client, appID, appSecret
	return client, nil
}

// resolveOpenDomain returns the OpenAPI base URL for the given brand. An
// empty or non-"lark" brand falls back to the Feishu domain.
func resolveOpenDomain(brand string) string {
	if strings.EqualFold(brand, "lark") {
		return lark.LarkBaseUrl
	}
	return lark.FeishuBaseUrl
}

func (l *Listener) newOpenClient() (*lark.Client, error) {
	return l.openClient.Get(l.configProvider, l.brand)
}

func parseLarkMillis(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// truncateForLog returns at most n bytes of b, with an ellipsis marker when
// truncated. Used to keep error log previews bounded for oversized / unknown
// event bodies.
func truncateForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}

func isUnsupportedEventLog(msg string) bool {
	return strings.Contains(msg, "event type:") && strings.Contains(msg, "not found handler")
}

// recoverableWSDisconnectMarkers are substrings emitted by the Lark SDK's
// Error logger for WebSocket faults that ws_runtime.go already handles via
// its own reconnect loop. They map to expected lifecycle events (peer-side
// close, network blip, our own shutdown closing the conn) rather than to a
// real backend failure, and should not be surfaced as ERROR.
var recoverableWSDisconnectMarkers = []string{
	"close 1000", // normal closure
	"close 1001", // going away (server restart / browser nav)
	"close 1006", // abnormal closure (no close frame)
	"close 1011", // server transient internal error
	"close 1012", // service restart
	"close 1013", // try again later
	"abnormal closure",
	"unexpected EOF",
	"use of closed network connection", // ws_runtime closed the conn during shutdown
	"i/o timeout",                      // read/write deadline exceeded; the runtime reconnects
	"connection reset by peer",
	"broken pipe",
}

func isRecoverableWSDisconnectLog(msg string) bool {
	if !strings.Contains(msg, "receive message failed") &&
		!strings.Contains(msg, "websocket") &&
		!strings.Contains(msg, "ws ") {
		return false
	}
	for _, m := range recoverableWSDisconnectMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// sensitiveQueryKeys are query-string parameter names whose values must never
// reach persisted logs. The Lark SDK prints its full WebSocket URL at Info
// level on (re)connect — see "connected to wss://...?access_key=...&ticket=..."
// — and access_key / ticket are the two credentials carried in that URL.
var sensitiveQueryKeys = []string{"access_key", "ticket"}

// redactSensitiveParams masks known-sensitive query-parameter values embedded
// in an arbitrary log string. It scans for "<key>=" occurrences and replaces
// the value up to the next "&" or non-URL-value delimiter with "***".
func redactSensitiveParams(s string) string {
	for _, k := range sensitiveQueryKeys {
		s = maskQueryParam(s, k)
	}
	return s
}

func maskQueryParam(s, key string) string {
	needle := key + "="
	if !strings.Contains(s, needle) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	rest := s
	for {
		i := strings.Index(rest, needle)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i+len(needle)])
		end := i + len(needle)
		for end < len(rest) {
			c := rest[end]
			if c == '&' || c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
				c == '[' || c == ']' || c == '"' || c == ',' {
				break
			}
			end++
		}
		b.WriteString("***")
		rest = rest[end:]
	}
}
