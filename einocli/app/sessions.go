package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	acp "github.com/eino-contrib/acp"
	"github.com/fanlv/quartet/einocli/config"
	"github.com/fanlv/quartet/einocli/logger"
	"github.com/fanlv/quartet/einocli/runtime"
	"github.com/google/uuid"
)

// sessionMeta is the on-disk per-session state, stored as meta.json inside
// the session directory ($EINO_HOME/sessions/<id>/). It is the only
// session-level record eino-cli keeps: conversation history lives in the
// same directory's messages.jsonl (owned by einocli/store).
type sessionMeta struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	// ModelID is the catalog id of the session's model; "" means "no model
	// selected" and prompts fail until set_config_option picks one.
	ModelID string `json:"model_id"`
	// ThinkingOverride is the session-level thought_level selection
	// (auto|enable|disable). Empty means "use the model's thinking_type".
	ThinkingOverride string `json:"thinking_override,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

// sessionState is the in-memory view of one session: its meta (canonical —
// every mutation persists through writeMetaLocked), the lazily-built cached
// runtime, and the in-flight prompt's cancel func.
type sessionState struct {
	mu sync.Mutex

	meta *sessionMeta
	dir  string

	// rt is the cached eino runtime, valid only while rtKey matches the
	// fingerprint of the next prompt's inputs (workdir + model config +
	// system prompt). A model or thought_level switch changes the key and
	// forces a rebuild — the runtime binds its chat model at New() time and
	// cannot hot-swap it.
	rt    *runtime.Agent
	rtKey string

	// promptCancel cancels the in-flight prompt turn; nil when idle.
	// session/cancel fires it so the blocked session/prompt RPC returns
	// stopReason=cancelled.
	promptCancel context.CancelFunc
}

const metaFileName = "meta.json"

// sanitizeSessionID rejects anything that is not a plain basename. Session
// ids are path components, so path traversal must fail before any disk I/O.
func sanitizeSessionID(id string) error {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return acp.ErrInvalidParams(fmt.Sprintf("invalid session id %q", id))
	}
	return nil
}

func sessionDir(id string) string {
	return filepath.Join(config.SessionsDir(), id)
}

// writeMetaLocked persists meta atomically with 0600 permissions (the
// directory may sit next to files holding API keys via the model catalog).
// Caller must hold st.mu.
func writeMetaLocked(dir string, meta *sessionMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session meta failed: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session dir failed: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-meta-*")
	if err != nil {
		return fmt.Errorf("create temp meta file failed: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write session meta failed: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod session meta failed: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close session meta failed: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, metaFileName)); err != nil {
		return fmt.Errorf("rename session meta failed: %w", err)
	}
	return nil
}

// loadMeta reads meta.json from dir. A missing file is an error (callers map
// it to "unknown session").
func loadMeta(dir string) (*sessionMeta, error) {
	data, err := os.ReadFile(filepath.Join(dir, metaFileName))
	if err != nil {
		return nil, err
	}
	var meta sessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal session meta failed: %w", err)
	}
	return &meta, nil
}

// unknownSessionErr is the canonical invalid-params error for an id with no
// persisted meta on disk.
func unknownSessionErr(id string) *acp.RPCError {
	return acp.ErrInvalidParams(fmt.Sprintf("unknown session %q", id))
}

// getState returns the in-memory state for id, or nil when this process has
// never seen the session.
func (a *Agent) getState(id string) *sessionState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

// getOrLoadState returns the session state, lazily loading meta from disk on
// first touch so every session RPC works right after a process restart.
func (a *Agent) getOrLoadState(id string) (*sessionState, error) {
	if err := sanitizeSessionID(id); err != nil {
		return nil, err
	}
	if st := a.getState(id); st != nil {
		return st, nil
	}

	dir := sessionDir(id)
	meta, err := loadMeta(dir)
	if err != nil {
		return nil, unknownSessionErr(id)
	}
	st := &sessionState{meta: meta, dir: dir}

	a.mu.Lock()
	// Re-check under the lock: a concurrent RPC for the same id may have
	// registered first; keep the winner so runtime/cancel state is shared.
	if existing, ok := a.sessions[id]; ok {
		a.mu.Unlock()
		return existing, nil
	}
	a.sessions[id] = st
	a.mu.Unlock()
	return st, nil
}

// newSession creates and registers a fresh session rooted at cwd.
func (a *Agent) newSession(cwd string) (*sessionState, error) {
	id := uuid.NewString()
	dir := sessionDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session dir failed: %w", err)
	}
	now := time.Now().Unix()
	meta := &sessionMeta{
		SessionID: id,
		Cwd:       cwd,
		ModelID:   defaultModelID(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	st := &sessionState{meta: meta, dir: dir}
	st.mu.Lock()
	err := writeMetaLocked(dir, meta)
	st.mu.Unlock()
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	a.sessions[id] = st
	a.mu.Unlock()
	return st, nil
}

// defaultModelID returns the first model in the catalog, or "" when the
// catalog is empty (the session then reports currentValue "" until
// set_config_option selects a model).
func defaultModelID() string {
	models, err := config.ListModels()
	if err != nil || len(models) == 0 {
		return ""
	}
	return models[0].ID
}

// metaToucher implements chatctx.SessionToucher by bumping meta.updated_at.
// Best-effort: a failed write is logged by the caller (chatctx treats Touch
// errors as non-fatal).
type metaToucher struct{ st *sessionState }

func (t metaToucher) Touch(_ string) error {
	t.st.mu.Lock()
	defer t.st.mu.Unlock()
	t.st.meta.UpdatedAt = time.Now().Unix()
	return writeMetaLocked(t.st.dir, t.st.meta)
}

// NewSession creates a fresh session: a uuid id, its directory under
// $EINO_HOME/sessions/, and the initial meta.json (default model = first
// catalog entry, "" when the catalog is empty).
func (a *Agent) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	cwd := req.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return acp.NewSessionResponse{}, acp.ErrInternalError(fmt.Sprintf("resolve cwd failed: %v", err), nil)
		}
	}
	st, err := a.newSession(cwd)
	if err != nil {
		return acp.NewSessionResponse{}, acp.ErrInternalError(err.Error(), nil)
	}
	opts, err := a.configOptions(st)
	if err != nil {
		return acp.NewSessionResponse{}, acp.ErrInternalError(err.Error(), nil)
	}
	logger.Infof(ctx, "[acp] session created: id=%s cwd=%s", st.meta.SessionID, cwd)
	return acp.NewSessionResponse{
		SessionID:     acp.SessionID(st.meta.SessionID),
		ConfigOptions: opts,
	}, nil
}

// ResumeSession restores a session WITHOUT replaying history. Everything is
// lazy-loaded (meta from disk, runtime on first prompt), so this works right
// after a process restart.
func (a *Agent) ResumeSession(ctx context.Context, req acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	st, err := a.getOrLoadState(string(req.SessionID))
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	opts, err := a.configOptions(st)
	if err != nil {
		return acp.ResumeSessionResponse{}, acp.ErrInternalError(err.Error(), nil)
	}
	logger.Infof(ctx, "[acp] session resumed: id=%s", st.meta.SessionID)
	return acp.ResumeSessionResponse{ConfigOptions: opts}, nil
}

// SessionCancel cancels the in-flight prompt for the session. It overrides
// BaseAgent's default (which errors): a cancel with no prompt in flight is a
// tolerated no-op.
func (a *Agent) SessionCancel(ctx context.Context, n acp.CancelNotification) error {
	st := a.getState(string(n.SessionID))
	if st == nil {
		return nil
	}
	st.mu.Lock()
	cancel := st.promptCancel
	st.mu.Unlock()
	if cancel != nil {
		logger.Infof(ctx, "[acp] cancel in-flight prompt: session=%s", st.meta.SessionID)
		cancel()
	}
	return nil
}
