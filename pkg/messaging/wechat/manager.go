package wechat

import (
	"context"
	"sync"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
)

// Manager owns the lifecycle of the WeChat listener goroutine. It mirrors
// pkg/messaging/lark.Manager — the same Start/Restart/Stop semantics so upstream
// handler code can treat the two platforms symmetrically.
type Manager struct {
	mu            sync.Mutex
	cancel        context.CancelFunc
	parentCtx     context.Context
	handler       messaging.EventHandler
	replier       *Replier
	credsProvider CredentialsProvider

	// listener is set whenever startLocked spins one up; used by IsExpired
	// so external callers can peek at the session-expired flag without
	// waiting on a mutex round trip.
	listener *Listener
}

// NewManager wires the Manager to the shared imGateway (as handler) and the
// Replier that will be used for every reply. The Replier must be the same
// instance registered with the imGateway — it doubles as the incoming-msg
// metadata cache (see Replier.RegisterIncoming in listener.go).
func NewManager(handler messaging.EventHandler, replier *Replier, credsProvider CredentialsProvider) *Manager {
	return &Manager{
		handler:       handler,
		replier:       replier,
		credsProvider: credsProvider,
	}
}

// Start kicks off the listener if credentials are present. Never blocks:
// the long-poll loop runs on its own goroutine. When no credentials are
// saved yet (pre-login), logs an info line and returns — Restart() brings
// the listener up after scan-to-login persists a Credentials file.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parentCtx = ctx
	m.startLocked()
}

func (m *Manager) startLocked() {
	// Idempotent: if a listener goroutine is already running, stop it first
	// so a second Start() can't orphan the previous goroutine by overwriting
	// m.cancel. Restart() already does this, but keeping startLocked itself
	// safe guards against future callers.
	m.stopLocked()
	if m.parentCtx == nil {
		return
	}
	// Drop cached clients and per-session reply metadata before reading the
	// latest credentials. This is important for logout too: after the account
	// file is removed credsProvider returns empty, but old in-flight jobs may
	// still finish later and attempt ReplyText(messageID). They must not reuse
	// a stale client/context from the previous login session.
	m.replier.ResetClients()
	creds := m.credsProvider()
	if len(creds) == 0 {
		logger.Info("[wechat] no credentials configured, skipping listener start")
		return
	}

	listenerCtx, cancel := context.WithCancel(m.parentCtx)
	m.cancel = cancel

	listener := NewListener(m.handler, m.replier, m.credsProvider)
	m.listener = listener

	go func() {
		if err := listener.Start(listenerCtx); err != nil {
			if listenerCtx.Err() == nil {
				logger.Error("[wechat] listener error: %v", err)
			}
		}
	}()
}

func (m *Manager) stopLocked() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.listener = nil
}

// Restart tears down the current listener goroutine and starts a fresh one
// from the latest credentials. Called by wechat_login_api.go after saving
// new credentials (login) or removing them (logout).
func (m *Manager) Restart() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	m.startLocked()
}

// Stop cancels the listener context. Called from the process shutdown path
// (cmd/web/main.go via Handler.StopIMListeners) so the iLink long-poll
// goroutine exits before HTTP shutdown completes, instead of being torn down
// implicitly when the root context cancels.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// IsExpired reports whether the current listener's session (bot_token) has
// been observed to be unrecoverable. Returns false when no listener is
// running (pre-login, post-logout).
func (m *Manager) IsExpired() bool {
	m.mu.Lock()
	l := m.listener
	m.mu.Unlock()
	return l != nil && l.IsExpired()
}
