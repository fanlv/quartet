// Package codexaccounts manages local Codex login profiles. Only display
// metadata is returned to callers; native Codex remains the token refresher.
package codexaccounts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
)

var ErrConflict = errors.New("Codex account state conflict")

type Options struct {
	Root        context.Context
	Environment func() map[string]string
	Acquire     func(context.Context) (func(), error)
	Invalidate  func(context.Context) error
}

type Service struct {
	mu              sync.Mutex
	repo            *repository.CodexAccountRepo
	options         Options
	recoveryError   error
	recoveryRelease func()
	logins          map[string]*loginAttempt
}

func NewService(options Options) (*Service, error) {
	repo, err := repository.NewCodexAccountRepo()
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, options: options, logins: map[string]*loginAttempt{}}, nil
}

// Initialize is intentionally separate: candidate startup checks never touch
// the host's active Codex files.
func (s *Service) Initialize(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.repo.Load()
	if err != nil {
		if s.recoveryRelease == nil {
			if release, acquireErr := s.options.Acquire(ctx); acquireErr == nil {
				s.recoveryRelease = release
			}
		}
		s.recoveryError = err
		return err
	}
	if state.Pending == nil {
		return nil
	}
	release, err := s.options.Acquire(ctx)
	if err != nil {
		return err
	}
	s.recoveryRelease = release
	if err := s.recover(state); err != nil {
		s.recoveryError = err
		return err
	}
	release()
	s.recoveryRelease = nil
	return nil
}

func (s *Service) home() (string, map[string]string, error) {
	env := map[string]string{}
	if s.options.Environment != nil {
		env = s.options.Environment()
	}
	home, err := typepath.CodexHome(env)
	return home, env, err
}

func (s *Service) List(ctx context.Context) (*model.CodexAccountResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.repo.Load()
	if err != nil {
		return nil, err
	}
	if state.Pending == nil && s.recoveryRelease != nil {
		s.recoveryRelease()
		s.recoveryRelease = nil
		s.recoveryError = nil
	}
	home, env, err := s.home()
	if err != nil {
		return nil, err
	}
	files, err := s.repo.ReadFiles(home)
	if err != nil {
		return nil, err
	}
	if state.Pending == nil {
		if changed := s.reconcile(state, files); changed {
			state.Revision++
			if err := s.repo.Save(state); err != nil {
				return nil, err
			}
		}
	}
	return s.response(state, files, env), nil
}

func (s *Service) response(state *repository.CodexAccountStore, files repository.CodexAccountFiles, env map[string]string) *model.CodexAccountResponse {
	result := &model.CodexAccountResponse{Code: 0, Revision: fmt.Sprint(state.Revision), LiveRevision: files.Revision(), Accounts: []model.CodexAccount{}, HasBackup: state.Backup != nil, Operations: state.Operations}
	for _, account := range state.Accounts {
		result.Accounts = append(result.Accounts, account.Info)
	}
	for _, attempt := range s.logins {
		if attempt.view.Status == "running" {
			view := attempt.view
			result.ActiveLogin = &view
			break
		}
	}
	current := model.CodexAccountCurrent{Home: files.Home, HasAuth: files.Auth != nil, HasConfig: files.Config != nil, Status: "missing"}
	if files.Auth != nil {
		info, identity, err := describe(*files.Auth, files.Config)
		if err != nil {
			current.Status = "invalid"
			current.Error = err.Error()
		} else {
			current.Account = &info
			current.Status = "unmanaged"
			if state.ActiveID != "" {
				current.Status = "external"
				for _, account := range state.Accounts {
					if account.Info.ID == state.ActiveID && account.Identity == identity && state.ActiveHome == files.Home {
						current.Status = "managed"
						current.ManagedID = account.Info.ID
						current.Account.ID, current.Account.Name, current.Account.Note = account.Info.ID, account.Info.Name, account.Info.Note
						current.ConfigApplied = state.ConfigApplied
					}
				}
			}
		}
	}
	if err := applicationConflict(files.Config, env); err != nil {
		current.BlockedReason = err.Error()
	}
	if state.Pending != nil {
		current.BlockedReason = "An interrupted Codex file replacement needs recovery"
		if s.recoveryError != nil {
			current.BlockedReason += ": " + s.recoveryError.Error()
		}
		result.HasBackup = true
	}
	result.Current = current
	return result
}

// reconcile copies live rotations back only after checking user AND workspace.
// Equal original refresh tokens identify duplicate snapshots of one login;
// independently authenticated sessions for the same user are not overwritten.
func (s *Service) reconcile(state *repository.CodexAccountStore, files repository.CodexAccountFiles) bool {
	if files.Auth == nil || state.ActiveHome != files.Home {
		return false
	}
	_, identity, err := describe(*files.Auth, files.Config)
	if err != nil {
		return false
	}
	for i := range state.Accounts {
		account := &state.Accounts[i]
		if account.Info.ID != state.ActiveID || account.Identity != identity {
			continue
		}
		changed := false
		previous := account.Auth
		if previous != *files.Auth {
			account.Auth = *files.Auth
			account.Info.UpdatedAt = time.Now().UnixMilli()
			changed = true
			previousRefresh := refreshToken(previous)
			for j := range state.Accounts {
				other := &state.Accounts[j]
				if other.Info.ID != account.Info.ID && other.Identity == identity && previousRefresh != "" && refreshToken(other.Auth) == previousRefresh {
					other.Auth = *files.Auth
					other.Info.UpdatedAt = account.Info.UpdatedAt
				}
			}
		}
		if state.ConfigApplied && !sameText(account.Config, files.Config) {
			account.Config = files.Config
			account.Info.UpdatedAt = time.Now().UnixMilli()
			changed = true
		}
		if changed {
			info, _, _ := describe(account.Auth, account.Config)
			info.ID, info.Name, info.Note = account.Info.ID, account.Info.Name, account.Info.Note
			info.CreatedAt, info.UpdatedAt, info.LastUsedAt = account.Info.CreatedAt, account.Info.UpdatedAt, account.Info.LastUsedAt
			account.Info = info
		}
		return changed
	}
	return false
}

func (s *Service) Mutate(ctx context.Context, action, id string, request model.CodexAccountMutation) (*model.CodexAccountResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := uuid.Parse(request.OperationID); err != nil {
		return nil, fmt.Errorf("operation_id must be a UUID: %w", err)
	}
	state, err := s.repo.Load()
	if err != nil {
		return nil, err
	}
	home, env, err := s.home()
	if err != nil {
		return nil, err
	}
	files, err := s.repo.ReadFiles(home)
	if err != nil {
		return nil, err
	}
	for _, operation := range state.Operations {
		if operation.ID == request.OperationID {
			if operation.Action != action || operation.AccountID != id {
				return nil, fmt.Errorf("%w: operation_id was already used", ErrConflict)
			}
			if operation.Status != "completed" {
				return nil, fmt.Errorf("%w: operation %s was %s: %s", ErrConflict, operation.ID, operation.Status, operation.Error)
			}
			if previous := state.OperationHashes[operation.ID]; previous != "" && previous != mutationHash(action, id, request) {
				return nil, fmt.Errorf("%w: operation_id was already used with a different request", ErrConflict)
			}
			return s.response(state, files, env), nil
		}
	}
	if state.Pending != nil {
		if action != "restore" {
			return nil, fmt.Errorf("%w: interrupted replacement requires recovery", ErrConflict)
		}
		if s.recoveryRelease == nil {
			s.recoveryRelease, err = s.options.Acquire(ctx)
			if err != nil {
				return nil, err
			}
		}
		if err := s.recover(state); err != nil {
			s.recoveryError = err
			return nil, err
		}
		s.recoveryRelease()
		s.recoveryRelease = nil
		s.recoveryError = nil
		files, err = s.repo.ReadFiles(home)
		if err != nil {
			return nil, err
		}
		return s.response(state, files, env), nil
	}
	if request.Revision != fmt.Sprint(state.Revision) || request.LiveRevision != files.Revision() {
		return nil, fmt.Errorf("%w: account records or local Codex files changed; refresh and retry", ErrConflict)
	}
	if state.OperationHashes == nil {
		state.OperationHashes = map[string]string{}
	}
	state.OperationHashes[request.OperationID] = mutationHash(action, id, request)
	index := -1
	for i := range state.Accounts {
		if state.Accounts[i].Info.ID == id {
			index = i
			break
		}
	}
	if id != "" && index < 0 {
		return nil, fmt.Errorf("Codex account %q does not exist", id)
	}
	operation := model.CodexAccountOperation{ID: request.OperationID, Action: action, AccountID: id, Status: "completed"}
	var candidate repository.StoredCodexAccount
	switch action {
	case "import", "capture", "update":
		if strings.TrimSpace(request.Name) == "" {
			return nil, fmt.Errorf("account name is required")
		}
		if index >= 0 {
			candidate = state.Accounts[index]
		} else {
			candidate.Info.ID = uuid.NewString()
			candidate.Info.CreatedAt = time.Now().UnixMilli()
		}
		candidate.Info.Name, candidate.Info.Note = strings.TrimSpace(request.Name), strings.TrimSpace(request.Note)
		if action == "capture" {
			if files.Auth == nil {
				return nil, fmt.Errorf("Codex auth.json does not exist in %s", home)
			}
			candidate.Auth, candidate.Config = *files.Auth, files.Config
		} else {
			if request.AuthJSON != nil {
				candidate.Auth = *request.AuthJSON
			}
			if request.ConfigTOML != nil {
				candidate.Config = request.ConfigTOML
			}
			if request.RemoveConfig {
				candidate.Config = nil
			}
		}
		info, identity, err := describe(candidate.Auth, candidate.Config)
		if err != nil {
			return nil, err
		}
		info.ID, info.Name, info.Note = candidate.Info.ID, candidate.Info.Name, candidate.Info.Note
		info.CreatedAt, info.UpdatedAt, info.LastUsedAt = candidate.Info.CreatedAt, time.Now().UnixMilli(), candidate.Info.LastUsedAt
		candidate.Info, candidate.Identity = info, identity
		for _, existing := range state.Accounts {
			if existing.Info.ID != candidate.Info.ID && existing.Auth == candidate.Auth && sameText(existing.Config, candidate.Config) {
				return nil, fmt.Errorf("identical Codex files already exist as account %q (%s); update that record instead", existing.Info.Name, existing.Info.ID)
			}
		}
	case "activate", "delete":
		if index < 0 {
			return nil, fmt.Errorf("account id is required")
		}
		candidate = state.Accounts[index]
	case "restore":
		if state.Backup == nil {
			return nil, fmt.Errorf("no Codex recovery snapshot exists")
		}
	default:
		return nil, fmt.Errorf("unsupported Codex account action %q", action)
	}
	currentID := s.response(state, files, env).Current.ManagedID
	filesChanged := action == "activate" || action == "restore" || action == "update" && id != "" && id == currentID && (request.AuthJSON != nil || request.ConfigTOML != nil || request.RemoveConfig)
	if filesChanged {
		release, err := s.options.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer func() {
			if s.recoveryError != nil {
				s.recoveryRelease = release
			} else {
				release()
			}
		}()
		// The execution gate is now held; re-read after closing idle processes.
		latest, err := s.repo.ReadFiles(home)
		if err != nil {
			return nil, err
		}
		if latest.Revision() != files.Revision() {
			return nil, fmt.Errorf("%w: Codex files changed while acquiring execution access; refresh and retry", ErrConflict)
		}
		s.reconcile(state, files)
		if action == "activate" {
			candidate = state.Accounts[index]
		}
		if action == "update" && request.AuthJSON == nil {
			candidate.Auth = state.Accounts[index].Auth
		}
		after := files
		nextID, applyConfig := candidate.Info.ID, request.ApplyConfig
		if action == "update" {
			applyConfig = state.ConfigApplied && !request.RemoveConfig
		}
		if action == "restore" {
			after = state.Backup.Files
			if after.Home != home {
				return nil, fmt.Errorf("recovery snapshot belongs to %s, current Codex home is %s", after.Home, home)
			}
			nextID, applyConfig = state.Backup.ManagedID, state.Backup.ConfigApplied
		} else {
			after.Auth = &candidate.Auth
			if applyConfig {
				if candidate.Config == nil {
					return nil, fmt.Errorf("this account has no config.toml; choose login-only switching")
				}
				after.Config = candidate.Config
			}
		}
		if action != "restore" {
			if err := applicationConflict(after.Config, env); err != nil {
				return nil, err
			}
			if _, _, err := describe(*after.Auth, after.Config); err != nil {
				return nil, err
			}
		}
		before := repository.CodexAccountBackup{Files: files, ManagedID: state.ActiveID, ConfigApplied: state.ConfigApplied}
		if s.response(state, files, env).Current.ManagedID == "" {
			before.ManagedID = ""
		}
		transaction := &repository.CodexAccountTransaction{Before: before, After: after, Operation: operation}
		state.Pending = transaction
		state.Revision++
		if err := s.repo.Save(state); err != nil {
			return nil, err
		}
		if s.options.Invalidate != nil {
			err = s.options.Invalidate(ctx)
		}
		if err == nil {
			err = s.repo.ReplaceFiles(files, after)
		}
		if err != nil {
			return nil, s.failedTransaction(err)
		}
		files = after
		state.Backup, state.Pending = &before, nil
		state.ActiveID, state.ActiveHome, state.ConfigApplied = nextID, home, applyConfig
		if action == "restore" {
			found := false
			for _, account := range state.Accounts {
				if account.Info.ID == nextID {
					found = true
					break
				}
			}
			if !found {
				state.ActiveID = ""
				state.ConfigApplied = false
			}
		}
		if action == "activate" {
			state.Accounts[index].Info.LastUsedAt = time.Now().UnixMilli()
		}
	}
	switch action {
	case "import", "capture":
		state.Accounts = append(state.Accounts, candidate)
		if action == "capture" {
			state.ActiveID, state.ActiveHome, state.ConfigApplied = candidate.Info.ID, home, candidate.Config != nil
		}
	case "update":
		state.Accounts[index] = candidate
	case "delete":
		state.Accounts = append(state.Accounts[:index], state.Accounts[index+1:]...)
		if state.ActiveID == id {
			state.ActiveID = ""
			state.ConfigApplied = false
		}
	}
	state.Revision++
	appendOperation(state, operation)
	if err := s.repo.Save(state); err != nil {
		if filesChanged {
			return nil, s.failedTransaction(err)
		}
		return nil, err
	}
	return s.response(state, files, env), nil
}

func appendOperation(state *repository.CodexAccountStore, operation model.CodexAccountOperation) {
	operation.FinishedAt = time.Now().UnixMilli()
	state.Operations = append(state.Operations, operation)
	if len(state.Operations) > 32 {
		for _, expired := range state.Operations[:len(state.Operations)-32] {
			delete(state.OperationHashes, expired.ID)
		}
		state.Operations = state.Operations[len(state.Operations)-32:]
	}
}

func mutationHash(action, id string, request model.CodexAccountMutation) string {
	raw, _ := json.Marshal(struct {
		Action  string
		ID      string
		Request model.CodexAccountMutation
	}{action, id, request})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Service) recover(state *repository.CodexAccountStore) error {
	if state.Pending == nil {
		return nil
	}
	if err := s.repo.Rollback(state.Pending); err != nil {
		return fmt.Errorf("restore interrupted Codex file replacement: %w", err)
	}
	operation := state.Pending.Operation
	operation.Status = "rolled_back"
	if operation.Error == "" {
		operation.Error = "Interrupted file replacement was restored"
	}
	state.ActiveID, state.ActiveHome, state.ConfigApplied = state.Pending.Before.ManagedID, state.Pending.Before.Files.Home, state.Pending.Before.ConfigApplied
	state.Pending = nil
	state.Revision++
	appendOperation(state, operation)
	return s.repo.Save(state)
}

func (s *Service) failedTransaction(cause error) error {
	state, err := s.repo.Load()
	if err != nil {
		s.recoveryError = errors.Join(cause, err)
		return s.recoveryError
	}
	if state.Pending != nil {
		state.Pending.Operation.Error = cause.Error()
		if err := s.repo.Save(state); err != nil {
			cause = errors.Join(cause, fmt.Errorf("save failed Codex replacement diagnostic: %w", err))
		}
	}
	if err := s.recover(state); err != nil {
		s.recoveryError = errors.Join(cause, err)
		return s.recoveryError
	}
	s.recoveryError = nil
	return fmt.Errorf("Codex file replacement failed; previous files restored: %w", cause)
}

func sameText(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func refreshToken(raw string) string {
	var auth struct {
		Tokens struct {
			Refresh string `json:"refresh_token"`
		} `json:"tokens"`
	}
	_ = json.Unmarshal([]byte(raw), &auth)
	return auth.Tokens.Refresh
}

func describe(auth string, config *string) (model.CodexAccount, string, error) {
	info := model.CodexAccount{HasConfig: config != nil}
	if config != nil {
		var parsed map[string]any
		if err := toml.Unmarshal([]byte(*config), &parsed); err != nil {
			return info, "", fmt.Errorf("parse config.toml: %w", err)
		}
		info.Model, _ = parsed["model"].(string)
		info.Provider, _ = parsed["model_provider"].(string)
	}
	var parsed struct {
		Mode   string `json:"auth_mode"`
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			ID      string `json:"id_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(auth), &parsed); err != nil {
		return info, "", fmt.Errorf("parse auth.json: %w", err)
	}
	if strings.TrimSpace(parsed.APIKey) != "" {
		info.AuthType = "api_key"
		sum := sha256.Sum256([]byte(parsed.APIKey))
		return info, "api:" + hex.EncodeToString(sum[:]), nil
	}
	if parsed.Tokens == nil || parsed.Tokens.Access == "" || parsed.Tokens.Refresh == "" || parsed.Tokens.ID == "" {
		return info, "", fmt.Errorf("auth.json must contain OPENAI_API_KEY or tokens.access_token, tokens.refresh_token and tokens.id_token")
	}
	if parsed.Mode != "" && parsed.Mode != "chatgpt" {
		return info, "", fmt.Errorf("unsupported file authentication mode %q; managed ChatGPT or API-key credentials are required", parsed.Mode)
	}
	parts := strings.Split(parsed.Tokens.ID, ".")
	if len(parts) != 3 {
		return info, "", fmt.Errorf("auth.json tokens.id_token must be a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return info, "", fmt.Errorf("decode auth.json ID-token claims: %w", err)
	}
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Auth    struct {
			User    string `json:"chatgpt_user_id"`
			Account string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return info, "", fmt.Errorf("parse auth.json ID-token claims: %w", err)
	}
	user := claims.Auth.User
	if user == "" {
		user = claims.Subject
	}
	workspace := parsed.Tokens.Account
	if workspace == "" {
		workspace = claims.Auth.Account
	}
	if user == "" || workspace == "" {
		return info, "", fmt.Errorf("auth.json must identify both the ChatGPT user and workspace")
	}
	info.AuthType, info.Email, info.AccountID, info.WorkspaceID = "chatgpt", claims.Email, user, workspace
	return info, "chatgpt:" + user + "|workspace:" + workspace, nil
}

func applicationConflict(config *string, env map[string]string) error {
	for _, key := range []string{consts.EnvKeyOpenAIAPIKey, consts.EnvKeyCodexAPIKey, consts.EnvKeyOpenAIIdentityTokenFile, consts.EnvKeyOpenAIAuthProvider} {
		value, exists := env[key]
		if !exists {
			value = os.Getenv(key)
		}
		if value != "" {
			return fmt.Errorf("%s overrides file-based Codex authentication; remove the override in Agent environment settings or the backend environment before switching", key)
		}
	}
	if config == nil {
		return nil
	}
	var parsed map[string]any
	if err := toml.Unmarshal([]byte(*config), &parsed); err != nil {
		return fmt.Errorf("parse config.toml: %w", err)
	}
	if mode, ok := parsed["cli_auth_credentials_store"].(string); ok && mode != "file" {
		return fmt.Errorf("config.toml cli_auth_credentials_store=%q; file-based account switching requires file", mode)
	}
	return nil
}
