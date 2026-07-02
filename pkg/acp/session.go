package acp

import (
	"context"
	"errors"
	"fmt"
	"os"

	acp "github.com/eino-contrib/acp"
	acptransport "github.com/eino-contrib/acp/transport"

	"github.com/fanlv/quartet/pkg/json"
	"github.com/fanlv/quartet/pkg/logger"
)

// IsBenignCloseErr reports whether err signals that the underlying ACP
// transport or JSON-RPC connection has already been closed. Best-effort
// notifications such as CancelSession on shutdown / run-switch routinely
// race the connection close (peer exit, idle reap, prior Conn.Close), and
// the resulting transport-closed / connection-closed return is part of the
// expected teardown — not a fault to alert on. Callers should downgrade
// the log level for these and keep WARN/ERROR for genuine RPC failures.
func IsBenignCloseErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, acptransport.ErrTransportClosed) ||
		errors.Is(err, acptransport.ErrConnClosed)
}

// SessionID aliases the SDK session id so callers can store it without
// importing the SDK directly.
type SessionID = acp.SessionID

// SessionResponse is the stable session view exposed by pkg/acp. Keep it
// independent from the SDK response shape so LoadSession does not need to
// synthesize a fake NewSessionResponse and callers do not have to choose
// between an embedded SDK SessionID and an outer shadowing field.
type SessionResponse struct {
	SessionID     string
	ConfigOptions []acp.SessionConfigOption
	Modes         *acp.SessionModeState
}

// SessionConfigSelect is a stable view of a select option in ConfigOptions,
// avoiding SDK types leaking into higher service layers.
type SessionConfigSelect struct {
	// ConfigID is the ACP config option id (e.g. "reasoning_effort") used to
	// set this value through SetSessionConfigOption. May be empty for agents
	// that omit it.
	ConfigID     string
	CurrentValue string
	Options      []SessionConfigSelectItem
}

type SessionConfigSelectItem struct {
	Description string
	Name        string
	Value       string
}

// ModelConfigSelect returns the model selector carried in ConfigOptions.
// Some ACP agents expose model choices there instead of filling Models.
func (r *SessionResponse) ModelConfigSelect() *SessionConfigSelect {
	if sel := r.configSelect(acp.SessionConfigOptionCategoryModel); sel != nil {
		return sel
	}
	// Positional fallback: claude-agent-acp puts model at index 1 without
	// setting category.
	return r.configSelectAt(1)
}

// ModeConfigSelect returns the mode selector carried in ConfigOptions.
// Some ACP agents expose mode choices there instead of filling Modes.
func (r *SessionResponse) ModeConfigSelect() *SessionConfigSelect {
	return r.configSelect(acp.SessionConfigOptionCategoryMode)
}

// ThoughtLevelConfigSelect returns the thought_level selector carried in
// ConfigOptions. Unlike mode, thought_level has no dedicated RPC, so callers
// drive it through SetSessionConfigOption using the select's ConfigID.
func (r *SessionResponse) ThoughtLevelConfigSelect() *SessionConfigSelect {
	return r.configSelect(acp.SessionConfigOptionCategoryThoughtLevel)
}

// configSelect extracts a select option from ConfigOptions matched by category.
func (r *SessionResponse) configSelect(category acp.SessionConfigOptionCategory) *SessionConfigSelect {
	if r == nil {
		return nil
	}
	for i := range r.ConfigOptions {
		selectOpt, ok := r.ConfigOptions[i].AsSelect()
		if !ok || !matchConfigOption(selectOpt, category) {
			continue
		}
		if out := buildConfigSelect(selectOpt); out != nil {
			return out
		}
	}
	return nil
}

// configSelectAt extracts the select option at the given index, ignoring
// category. Used as a positional fallback for agents that omit it.
func (r *SessionResponse) configSelectAt(idx int) *SessionConfigSelect {
	if r == nil || idx < 0 || idx >= len(r.ConfigOptions) {
		return nil
	}
	selectOpt, ok := r.ConfigOptions[idx].AsSelect()
	if !ok {
		return nil
	}
	return buildConfigSelect(selectOpt)
}

func buildConfigSelect(selectOpt acp.SessionConfigOptionSelect) *SessionConfigSelect {
	out := &SessionConfigSelect{
		CurrentValue: string(selectOpt.CurrentValue),
	}
	if selectOpt.ID != nil {
		out.ConfigID = string(*selectOpt.ID)
	}
	appendConfigSelectOptions(out, selectOpt.Options)
	if len(out.Options) == 0 {
		return nil
	}
	return out
}

func matchConfigOption(opt acp.SessionConfigOptionSelect, category acp.SessionConfigOptionCategory) bool {
	data, err := json.Marshal(opt)
	if err != nil {
		return false
	}
	var meta struct {
		Category string `json:"category"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return false
	}
	return meta.Category == string(category)
}

func appendConfigSelectOptions(out *SessionConfigSelect, opts acp.SessionConfigSelectOptions) {
	if opts.SessionConfigSelectOptionList != nil {
		for _, o := range *opts.SessionConfigSelectOptionList {
			appendConfigSelectOption(out, o)
		}
	}
	if opts.SessionConfigSelectGroupList != nil {
		for _, g := range *opts.SessionConfigSelectGroupList {
			for _, o := range g.Options {
				appendConfigSelectOption(out, o)
			}
		}
	}
}

func appendConfigSelectOption(out *SessionConfigSelect, o acp.SessionConfigSelectOption) {
	out.Options = append(out.Options, SessionConfigSelectItem{
		Description: o.Description,
		Name:        o.Name,
		Value:       string(o.Value),
	})
}

// NewSession creates a fresh ACP session on this connection.
func (c *Conn) NewSession(ctx context.Context, workdir string) (*SessionResponse, error) {
	cwd, err := resolveCwd(workdir)
	if err != nil {
		return nil, err
	}
	resp, err := c.conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        cwd,
		MCPServers: []acp.MCPServer{},
	})
	if err != nil {
		return nil, fmt.Errorf("acp new_session failed: %w, stderr: %s", err, c.stderrBuf.String())
	}
	logger.Debugf(ctx, "[ACP] session created: %s cwd=%s", resp.SessionID, cwd)
	return &SessionResponse{
		SessionID:     string(resp.SessionID),
		ConfigOptions: resp.ConfigOptions,
		Modes:         resp.Modes,
	}, nil
}

// LoadSession restores an existing ACP session on this connection.
func (c *Conn) LoadSession(ctx context.Context, sessionID, workdir string) (*SessionResponse, error) {
	cwd, err := resolveCwd(workdir)
	if err != nil {
		return nil, err
	}
	resp, err := c.conn.LoadSession(ctx, acp.LoadSessionRequest{
		SessionID:  acp.SessionID(sessionID),
		Cwd:        cwd,
		MCPServers: []acp.MCPServer{},
	})
	if err != nil {
		return nil, fmt.Errorf("acp load_session failed: %w, stderr: %s", err, c.stderrBuf.String())
	}
	logger.Debugf(ctx, "[ACP] session loaded: %s cwd=%s", sessionID, cwd)
	return &SessionResponse{
		SessionID:     sessionID,
		ConfigOptions: resp.ConfigOptions,
		Modes:         resp.Modes,
	}, nil
}

// SupportsResume reports whether the agent advertised the
// sessionCapabilities.resume capability during initialize. When true, the
// reconnect path can use ResumeSession instead of LoadSession to restore the
// session without replaying conversation history.
func (c *Conn) SupportsResume() bool {
	return c.supportsResume
}

// ResumeSession restores an existing ACP session on this connection WITHOUT
// replaying conversation history. Unlike LoadSession (which the protocol
// requires to re-stream every prior turn via session/update notifications
// before responding), session/resume restores the subprocess-side context
// and returns once the session is ready — no replay events are emitted, so
// the caller's stream handler never sees stale historical output.
//
// Only valid when SupportsResume reports true. The SDK method is marked
// UNSTABLE; callers MUST gate on the advertised capability and fall back to
// LoadSession when resume is unavailable.
func (c *Conn) ResumeSession(ctx context.Context, sessionID, workdir string) (*SessionResponse, error) {
	cwd, err := resolveCwd(workdir)
	if err != nil {
		return nil, err
	}
	resp, err := c.conn.ResumeSession(ctx, acp.ResumeSessionRequest{
		SessionID:  acp.SessionID(sessionID),
		Cwd:        cwd,
		MCPServers: []acp.MCPServer{},
	})
	if err != nil {
		return nil, fmt.Errorf("acp resume_session failed: %w, stderr: %s", err, c.stderrBuf.String())
	}
	logger.Debugf(ctx, "[ACP] session resumed: %s cwd=%s", sessionID, cwd)
	return &SessionResponse{
		SessionID:     sessionID,
		ConfigOptions: resp.ConfigOptions,
		Modes:         resp.Modes,
	}, nil
}

// PromptSlot is a held prompt slot on a Conn. While holding it, the caller
// has exclusive right to call SendPrompt on the underlying connection and is
// safely recorded as the connection's active prompt session. Callers install
// a stream handler AFTER acquiring the slot so that residue updates from the
// previous (cancelled) prompt cannot reach the new handler — see the race
// discussion in services/agent/acp/agent.go.Run.
type PromptSlot struct {
	conn     *Conn
	sid      SessionID
	released bool
}

// AcquirePromptSlot best-effort cancels any active prompt on this connection,
// waits for the prompt semaphore, records this session as the active prompt,
// and returns a slot handle. The slot MUST be Released by the caller
// (typically via defer) before any other prompt on this connection can proceed.
//
// cancelActivePromptTimeout only bounds the SessionCancel RPC itself; waiting
// for the previous prompt to release promptSem is governed by ctx. In the
// service path, ACPAgent.runSem ensures the previous Run has already unwound
// its handler cleanup before the next Run reaches this method.
//
// Splitting slot acquisition from prompt send lets callers install a stream
// handler in the interval between "old prompt released the slot" and
// "subprocess receives new prompt text", closing the residue-delivery race
// described in ACPAgent.Run.
func (c *Conn) AcquirePromptSlot(ctx context.Context, sessionID SessionID) (*PromptSlot, error) {
	// Cancel the previous prompt under a bounded timeout decoupled from the
	// caller's ctx — using context.Background() here used to let a stuck
	// subprocess freeze the entire Stop / run-switch path. WithoutCancel +
	// timeout means: respect our own timeout, but do NOT inherit the
	// caller's cancellation (we still want to send the Cancel even when the
	// caller is in a Stop flow).
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelActivePromptTimeout)
	c.CancelActivePrompt(cancelCtx)
	cancel()
	select {
	case c.promptSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.setActiveSession(sessionID)
	return &PromptSlot{conn: c, sid: sessionID}, nil
}

// Release returns the slot to the connection. Idempotent — subsequent calls
// are no-ops — so callers can use both defer and explicit release.
func (s *PromptSlot) Release() {
	if s.released {
		return
	}
	s.released = true
	s.conn.clearActiveSession(s.sid)
	<-s.conn.promptSem
}

// SendPrompt sends the prompt text on the held slot and blocks until the
// agent finishes the turn. Must be called after AcquirePromptSlot and before
// Release.
func (s *PromptSlot) SendPrompt(ctx context.Context, text string) error {
	_, err := s.conn.conn.Prompt(ctx, acp.PromptRequest{
		SessionID: s.sid,
		Prompt:    []acp.ContentBlock{acp.NewContentBlockText(acp.TextContent{Text: text})},
	})
	return err
}

// CancelSession sends a Cancel notification for the specified session.
func (c *Conn) CancelSession(ctx context.Context, sessionID SessionID) error {
	return c.conn.SessionCancel(ctx, acp.CancelNotification{SessionID: sessionID})
}

// SetSessionMode switches the active mode for the session.
func (c *Conn) SetSessionMode(ctx context.Context, sessionID SessionID, mode string) error {
	resp, err := c.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionID: sessionID,
		ModeID:    acp.SessionModeID(mode),
	})
	if err != nil {
		return err
	}
	logger.Debugf(ctx, "[ACP] SetSessionMode mode=%s resp=%s", mode, json.String(resp))
	return nil
}

// SetSessionModel switches the active model for the session. The dedicated
// session/set_model RPC was removed from the ACP v1 schema, so model selection
// now always goes through the generic SetSessionConfigOption API keyed by the
// "model" config option.
func (c *Conn) SetSessionModel(ctx context.Context, sessionID SessionID, modelID string) error {
	configID := acp.SessionConfigID("model")
	value := acp.SessionConfigValueID(modelID)
	resp, err := c.conn.SetSessionConfigOption(ctx, acp.NewSetSessionConfigOptionRequestValueID(acp.SetSessionConfigOptionRequestValueID{
		SessionID: &sessionID,
		ConfigID:  &configID,
		Value:     &value,
	}))
	if err != nil {
		return err
	}
	logger.Infof(ctx, "[ACP] SetSessionModel modelID=%s resp=%s", modelID, json.String(resp))
	return nil
}

// SetSessionThoughtLevel switches the active thought_level for the session.
// thought_level has no dedicated RPC, so it always goes through the generic
// SetSessionConfigOption API keyed by the config option id (e.g.
// "reasoning_effort") discovered from the session's ConfigOptions.
func (c *Conn) SetSessionThoughtLevel(ctx context.Context, sessionID SessionID, configID, value string) error {
	cid := acp.SessionConfigID(configID)
	val := acp.SessionConfigValueID(value)
	resp, err := c.conn.SetSessionConfigOption(ctx, acp.NewSetSessionConfigOptionRequestValueID(acp.SetSessionConfigOptionRequestValueID{
		SessionID: &sessionID,
		ConfigID:  &cid,
		Value:     &val,
	}))
	if err != nil {
		return err
	}
	logger.Debugf(ctx, "[ACP] SetSessionThoughtLevel configID=%s value=%s resp=%s", configID, value, json.String(resp))
	return nil
}

func resolveCwd(workdir string) (string, error) {
	if workdir != "" {
		return workdir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	return cwd, nil
}
