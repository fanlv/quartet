package lark

import (
	"context"
	"runtime/debug"
	"sync"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
)

type Manager struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	parentCtx context.Context
	handler   messaging.EventHandler
	configFn  ConfigProvider
	listener  *Listener
	appID     string
	secret    string
}

func NewManager(handler messaging.EventHandler, configFn ConfigProvider) *Manager {
	return &Manager{
		handler:  handler,
		configFn: configFn,
	}
}

func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parentCtx = ctx
	m.startLocked()
}

func (m *Manager) startLocked() {
	if m.parentCtx == nil {
		return
	}
	appID, appSecret := m.configFn()
	if appID == "" || appSecret == "" {
		logger.Info("[lark] no credentials configured, skipping listener start")
		m.appID, m.secret = "", ""
		return
	}
	if m.listener != nil && m.appID == appID && m.secret == appSecret {
		logger.Debug("[lark] credentials unchanged, listener restart skipped")
		return
	}
	if m.listener != nil {
		m.stopLocked()
	}

	// listenerCtx is intentionally derived from context.Background(), not
	// m.parentCtx. m.parentCtx is the process signal-aware ctx; deriving the
	// listener ctx from it lets SIGINT cancel the listener ctx in parallel
	// with the main shutdown sequence — ws_runtime.go's <-ctx.Done() arm
	// then races Manager.Stop() to close the WebSocket, and the SDK's read
	// goroutine emits "use of closed network connection" via sdkLogger.Error
	// BEFORE listener.Stop() can set stopped=true to demote that line to
	// Debug. Decoupling makes the only cancellation path Manager.Stop() →
	// stopLocked() → listener.Stop() (sets stopped=true) → m.cancel(), which
	// is fully ordered in the main goroutine. m.parentCtx is still kept as a
	// "Start was called" sentinel below.
	listenerCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.appID, m.secret = appID, appSecret

	listener := NewListener(m.handler, m.configFn)
	m.listener = listener

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("[lark] listener goroutine panic: %v\n%s", r, debug.Stack())
			}
		}()
		if err := listener.Start(listenerCtx); err != nil {
			if listenerCtx.Err() == nil {
				logger.Error("[lark] listener error: %v", err)
			}
		}
	}()
}

func (m *Manager) stopLocked() {
	if m.listener != nil {
		m.listener.Stop()
		m.listener = nil
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.appID, m.secret = "", ""
}

func (m *Manager) Restart() {
	m.mu.Lock()
	defer m.mu.Unlock()
	appID, appSecret := m.configFn()
	if m.listener != nil && m.appID == appID && m.secret == appSecret {
		logger.Debug("[lark] credentials unchanged, restart skipped")
		return
	}
	m.stopLocked()
	m.startLocked()
}

// Stop cancels the current listener. Called from the process shutdown path
// (cmd/web/main.go via Handler.StopIMListeners) so the WebSocket close races
// with HTTP shutdown deterministically — without it the read loop's "use of
// closed network connection" log fires after the rest of the shutdown
// sequence has already moved on. Also mirrors pkg/messaging/wechat.Manager.Stop
// so upstream code can treat both platforms symmetrically.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}
