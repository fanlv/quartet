// Package authentication coordinates explicit ACP login without owning the
// agents' credential files. Discovery and background probes never authenticate.
package authentication

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/eino-contrib/acp"
	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/fileserver"
	"github.com/fanlv/quartet/services/agent/catalog"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/config"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

const loginTimeout = 10 * time.Minute

var ErrInProgress = errors.New("Agent authentication is already in progress")

type attempt struct {
	view       model.AgentAuthAttempt
	methods    []model.AgentAuthMethod
	cancel     context.CancelFunc
	linkBuffer string
	envVersion int64
	done       chan struct{}
}

type Service struct {
	root     context.Context
	catalog  *catalog.Service
	settings config.SettingsService
	cache    *probe.CacheService
	acquire  func(string) (func(), bool)
	mu       sync.Mutex
	attempts map[string]*attempt
}

func NewService(root context.Context, catalog *catalog.Service, settings config.SettingsService, cache *probe.CacheService, acquire func(string) (func(), bool)) *Service {
	return &Service{root: root, catalog: catalog, settings: settings, cache: cache, acquire: acquire, attempts: make(map[string]*attempt)}
}

func (s *Service) binding(ctx context.Context, agentID, revision string) (model.AgentRuntimeBinding, error) {
	resolved, found, err := s.catalog.ResolveBinding(ctx, agentID, "")
	if err != nil {
		return resolved, err
	}
	if !found {
		return resolved, fmt.Errorf("AgentID %q does not exist", agentID)
	}
	entry, _, err := s.catalog.Find(ctx, agentID)
	if err != nil {
		return resolved, err
	}
	if (entry.Builtin != nil && entry.Builtin.Deprecated) ||
		(entry.Custom != nil && entry.Custom.Lifecycle != model.AgentLifecycleActive) {
		return resolved, fmt.Errorf("AgentID %q is not active", agentID)
	}
	if revision != "" && revision != resolved.Revision {
		return resolved, fmt.Errorf("AgentID %q revision changed: requested=%q current=%q; refresh before authenticating", agentID, revision, resolved.Revision)
	}
	installed := (agentinstall.Checker{}).Check(agentinstall.Definition{Bin: resolved.Definition.Bin, ACPProgram: resolved.Definition.ACPProgram})
	if !installed.Installed {
		return resolved, fmt.Errorf("AgentID %q is not installed: %s", agentID, installed.Error)
	}
	return resolved, nil
}

func (s *Service) connect(ctx context.Context, binding model.AgentRuntimeBinding, output func(string), link func(string, string)) (*pkgacp.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := pkgacp.RegisterAgentRuntime(binding.RuntimeKey, pkgacp.RuntimeDefinition{
		Program: binding.Definition.ACPProgram, Args: binding.Definition.ACPArgs, EnvKey: binding.AgentID,
	}); err != nil {
		return nil, err
	}
	workdir, err := fileserver.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return pkgacp.NewAuthenticationConn(ctx, binding.RuntimeKey, workdir, output, link)
}

func (s *Service) Info(ctx context.Context, agentID string) (model.AgentAuthResponse, error) {
	binding, err := s.binding(ctx, agentID, "")
	if err != nil {
		return model.AgentAuthResponse{}, err
	}
	envVersion := s.settings.GetACPEnvVersion(agentID)
	s.mu.Lock()
	s.pruneLocked()
	previous := s.attempts[agentID]
	var previousView *model.AgentAuthAttempt
	if previous != nil && previous.view.Revision == binding.Revision && (previous.cancel != nil || previous.envVersion == envVersion) {
		copy := s.snapshotLocked(previous)
		previousView = &copy
		if previous.cancel != nil {
			methods := append([]model.AgentAuthMethod{}, previous.methods...)
			response := model.AgentAuthResponse{AgentID: agentID, Revision: binding.Revision, Methods: methods, Attempt: previousView}
			s.mu.Unlock()
			return response, nil
		}
	}
	s.mu.Unlock()
	release, acquired := s.acquire(agentID)
	if !acquired {
		return model.AgentAuthResponse{}, fmt.Errorf("AgentID %q is being deleted", agentID)
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := s.connect(ctx, binding, nil, nil)
	if err != nil {
		return model.AgentAuthResponse{}, err
	}
	defer conn.Close()
	return model.AgentAuthResponse{AgentID: agentID, Revision: binding.Revision, Methods: s.methods(binding, conn.AuthMethods()), Attempt: previousView}, nil
}

func (s *Service) methods(binding model.AgentRuntimeBinding, advertised []acpsdk.AuthMethod) []model.AgentAuthMethod {
	methods := make([]model.AgentAuthMethod, 0, len(advertised))
	for _, method := range advertised {
		view := model.AgentAuthMethod{}
		if v, ok := method.AsAgentVariant(); ok {
			view.ID, view.Name, view.Description, view.Type = string(v.ID), v.Name, v.Description, "agent"
			// Older adapters express API-key methods as agent authentication.
			if view.ID == "gemini-api-key" {
				view.Environment = []string{consts.EnvKeyGeminiAPIKey}
			}
			if view.ID == "api-key" && binding.AgentID == "codex" {
				view.Environment = []string{consts.EnvKeyOpenAIAPIKey}
			}
		} else if v, ok := method.AsTerminalVariant(); ok {
			view.ID, view.Name, view.Description, view.Type = string(v.ID), v.Name, v.Description, "terminal"
			program := binding.Definition.ACPProgram
			if path, err := executil.LookPath(program); err == nil {
				program = path
			}
			args := append(append([]string(nil), binding.Definition.ACPArgs...), v.Args...)
			view.Command = executil.ShellCommand(program, args, v.Env)
			for key := range s.settings.GetACPEnvVars(binding.AgentID) {
				view.Environment = append(view.Environment, key)
			}
			sort.Strings(view.Environment)
		} else if v, ok := method.AsEnvVarVariant(); ok {
			view.ID, view.Name, view.Description, view.Type = string(v.ID), v.Name, v.Description, "env_var"
			for _, variable := range v.Vars {
				view.Environment = append(view.Environment, variable.Name)
			}
		} else {
			continue
		}
		methods = append(methods, view)
	}
	return methods
}

// Start is idempotent for an already working login: verify first, authenticate
// only when session/new reports missing credentials, then verify again using a
// fresh process and the exact current environment version.
func (s *Service) Start(ctx context.Context, agentID string, request model.AgentAuthRequest) (model.AgentAuthAttempt, error) {
	if request.Revision == "" || request.MethodID == "" {
		return model.AgentAuthAttempt{}, fmt.Errorf("revision and method_id are required")
	}
	binding, err := s.binding(ctx, agentID, request.Revision)
	if err != nil {
		return model.AgentAuthAttempt{}, err
	}
	release, acquired := s.acquire(agentID)
	if !acquired {
		return model.AgentAuthAttempt{}, fmt.Errorf("AgentID %q is being deleted", agentID)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		release()
		return model.AgentAuthAttempt{}, err
	}
	loginCtx, cancel := context.WithTimeout(s.root, loginTimeout)
	envVersion := s.settings.GetACPEnvVersion(agentID)
	record := &attempt{view: model.AgentAuthAttempt{
		ID: hex.EncodeToString(id[:]), AgentID: agentID, Revision: binding.Revision,
		MethodID: request.MethodID, Status: "checking", StartedAt: time.Now().UnixMilli(),
	}, cancel: cancel, envVersion: envVersion, done: make(chan struct{})}
	s.mu.Lock()
	s.pruneLocked()
	if previous := s.attempts[agentID]; previous != nil && previous.cancel != nil {
		s.mu.Unlock()
		cancel()
		release()
		return model.AgentAuthAttempt{}, fmt.Errorf("%w: %q", ErrInProgress, agentID)
	}
	s.attempts[agentID] = record
	initial := s.snapshotLocked(record)
	s.mu.Unlock()
	go func() {
		defer close(record.done)
		defer release()
		defer cancel()
		err := s.run(loginCtx, binding, envVersion, record)
		s.mu.Lock()
		defer s.mu.Unlock()
		record.cancel = nil
		record.view.FinishedAt = time.Now().UnixMilli()
		if err != nil {
			record.view.Status, record.view.Error = "error", err.Error()
			if errors.Is(err, context.Canceled) {
				record.view.Status = "cancelled"
			}
		} else {
			record.view.Status = "available"
		}
	}()
	return initial, nil
}

func (s *Service) run(ctx context.Context, binding model.AgentRuntimeBinding, envVersion int64, record *attempt) error {
	validation, err := probe.ValidateBinding(ctx, binding, envVersion, nil)
	if persistErr := s.cache.PersistNow(ctx); persistErr != nil {
		return errors.Join(err, fmt.Errorf("persist ACP validation failed: %w", persistErr))
	}
	if err != nil && !validation.AuthenticationRequired {
		return err
	}
	if validation.Success {
		return s.ensureCurrent(ctx, binding, envVersion)
	}
	if err := s.ensureCurrent(ctx, binding, envVersion); err != nil {
		return err
	}
	conn, err := s.connect(ctx, binding, func(chunk string) {
		s.mu.Lock()
		record.view.Output += chunk
		record.linkBuffer += chunk
		lastNewline := strings.LastIndexByte(record.linkBuffer, '\n')
		var completeLines string
		if lastNewline >= 0 {
			completeLines = record.linkBuffer[:lastNewline+1]
			record.linkBuffer = record.linkBuffer[lastNewline+1:]
		}
		s.mu.Unlock()
		for _, raw := range authURLPattern.FindAllString(completeLines, -1) {
			s.addLink(record, raw, "")
		}
	}, func(url, message string) { s.addLink(record, url, message) })
	if err != nil {
		return err
	}
	methods := s.methods(binding, conn.AuthMethods())
	s.mu.Lock()
	record.methods = methods
	record.view.Status = "authenticating"
	s.mu.Unlock()
	// Close before verification: an OAuth flow abandoned by session/new must
	// never survive and compete with this explicit login's callback listener.
	err = conn.Authenticate(ctx, record.view.MethodID)
	conn.Close()
	s.mu.Lock()
	remainder := record.linkBuffer
	record.linkBuffer = ""
	s.mu.Unlock()
	for _, raw := range authURLPattern.FindAllString(remainder, -1) {
		s.addLink(record, raw, "")
	}
	if err != nil {
		return err
	}
	if err := s.ensureCurrent(ctx, binding, envVersion); err != nil {
		return err
	}
	s.mu.Lock()
	record.view.Status = "validating"
	s.mu.Unlock()
	_, err = probe.ValidateBinding(ctx, binding, envVersion, nil)
	if persistErr := s.cache.PersistNow(ctx); persistErr != nil {
		if err != nil {
			return fmt.Errorf("%w; persist ACP validation failed: %v", err, persistErr)
		}
		return fmt.Errorf("persist ACP validation failed: %w", persistErr)
	}
	if err != nil {
		return fmt.Errorf("authentication completed, but ACP validation failed: %w", err)
	}
	return s.ensureCurrent(ctx, binding, envVersion)
}

func (s *Service) ensureCurrent(ctx context.Context, binding model.AgentRuntimeBinding, envVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.binding(ctx, binding.AgentID, binding.Revision); err != nil {
		return err
	}
	if current := s.settings.GetACPEnvVersion(binding.AgentID); current != envVersion {
		return fmt.Errorf("AgentID %q environment changed during authentication: started=%d current=%d; revalidate the current configuration", binding.AgentID, envVersion, current)
	}
	return nil
}

var authURLPattern = regexp.MustCompile(`https?://[^\s<>"\x1b]+`)

func (s *Service) addLink(record *attempt, raw, message string) {
	parsed, err := url.Parse(strings.TrimRight(raw, "),.;'"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, link := range record.view.Links {
		if link.URL == parsed.String() {
			return
		}
	}
	record.view.Links = append(record.view.Links, model.AgentAuthLink{URL: parsed.String(), Message: message})
}

func (s *Service) Read(agentID, attemptID string) (model.AgentAuthAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	record := s.attempts[agentID]
	if record == nil || record.view.ID != attemptID {
		return model.AgentAuthAttempt{}, fmt.Errorf("Agent authentication attempt %q does not exist for %q", attemptID, agentID)
	}
	return s.snapshotLocked(record), nil
}

func (s *Service) Cancel(agentID, attemptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.attempts[agentID]
	if record == nil || record.view.ID != attemptID {
		return fmt.Errorf("Agent authentication attempt %q does not exist for %q", attemptID, agentID)
	}
	if record.cancel != nil {
		record.cancel()
	}
	return nil
}

func (s *Service) CancelAgent(ctx context.Context, agentID string) error {
	s.mu.Lock()
	record := s.attempts[agentID]
	if record == nil || record.cancel == nil {
		s.mu.Unlock()
		return nil
	}
	record.cancel()
	done := record.done
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for AgentID %q authentication to stop failed: %w", agentID, ctx.Err())
	}
}

func (s *Service) snapshotLocked(record *attempt) model.AgentAuthAttempt {
	view := record.view
	view.Links = append([]model.AgentAuthLink(nil), view.Links...)
	return view
}

func (s *Service) pruneLocked() {
	cutoff := time.Now().Add(-5 * time.Minute).UnixMilli()
	for agentID, record := range s.attempts {
		if record.cancel == nil && record.view.FinishedAt < cutoff {
			delete(s.attempts, agentID)
		}
	}
}
