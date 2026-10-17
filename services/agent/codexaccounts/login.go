package codexaccounts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
	"github.com/google/uuid"
)

type loginAttempt struct {
	view     model.CodexAccountLogin
	note     string
	cancel   context.CancelFunc
	finished time.Time
}

type loginOutput struct {
	service *Service
	attempt *loginAttempt
}

func (w loginOutput) Write(p []byte) (int, error) {
	w.service.mu.Lock()
	w.attempt.view.Output += string(p)
	w.service.mu.Unlock()
	return len(p), nil
}

// Login uses Codex's device-code flow in a separate home. Completing a new
// account login never signs out or replaces the currently active account.
func (s *Service) Login(request model.CodexAccountMutation) (model.CodexAccountLogin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := uuid.Parse(request.OperationID); err != nil {
		return model.CodexAccountLogin{}, fmt.Errorf("operation_id must be a UUID: %w", err)
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return model.CodexAccountLogin{}, fmt.Errorf("account name is required")
	}
	if previous := s.logins[request.OperationID]; previous != nil {
		if previous.view.Name != name || previous.note != request.Note {
			return model.CodexAccountLogin{}, fmt.Errorf("%w: operation_id was already used with a different login request", ErrConflict)
		}
		return previous.view, nil
	}
	for id, previous := range s.logins {
		if previous.cancel != nil {
			return model.CodexAccountLogin{}, fmt.Errorf("%w: Codex account login %s is already in progress", ErrConflict, id)
		}
		if time.Since(previous.finished) > time.Hour {
			delete(s.logins, id)
		}
	}
	_, env, err := s.home()
	if err != nil {
		return model.CodexAccountLogin{}, err
	}
	program := env[consts.EnvKeyCodexPath]
	if program == "" {
		program = os.Getenv(consts.EnvKeyCodexPath)
	}
	if program == "" {
		program = "codex"
	}
	program, err = executil.LookPath(program)
	if err != nil {
		return model.CodexAccountLogin{}, fmt.Errorf("find Codex CLI for device login: %w", err)
	}
	home, err := s.repo.PrepareLoginHome(request.OperationID)
	if err != nil {
		return model.CodexAccountLogin{}, err
	}
	root := s.options.Root
	if root == nil {
		root = context.Background()
	}
	ctx, cancel := context.WithTimeout(root, 10*time.Minute)
	attempt := &loginAttempt{view: model.CodexAccountLogin{ID: request.OperationID, Name: name, Status: "running"}, note: request.Note, cancel: cancel}
	cmd := executil.CommandContext(ctx, program, "login", "--device-auth")
	configureLoginProcess(cmd)
	cmd.Dir = home
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Env = append(cmd.Env, consts.EnvKeyCodexHome+"="+home)
	cmd.Stdout, cmd.Stderr = loginOutput{s, attempt}, loginOutput{s, attempt}
	if err := cmd.Start(); err != nil {
		cancel()
		cleanupErr := s.repo.RemoveLoginHome(request.OperationID)
		return model.CodexAccountLogin{}, errors.Join(fmt.Errorf("start Codex device login: %w", err), cleanupErr)
	}
	s.logins[request.OperationID] = attempt
	go func() {
		err := cmd.Wait()
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if err == nil {
			err = s.saveLogin(attempt, home)
		}
		if cleanupErr := s.repo.RemoveLoginHome(request.OperationID); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary Codex login credentials: %w", cleanupErr))
		}
		attempt.cancel = nil
		attempt.finished = time.Now()
		if err != nil {
			attempt.view.Status, attempt.view.Error = "error", err.Error()
			if errors.Is(err, context.Canceled) {
				attempt.view.Status = "cancelled"
			}
			logger.Warnf(root, "[codex.accounts] device login ended: %v", err)
		} else {
			attempt.view.Status = "saved"
		}
	}()
	return attempt.view, nil
}

func (s *Service) saveLogin(attempt *loginAttempt, home string) error {
	files, err := s.repo.ReadFiles(home)
	if err != nil {
		return err
	}
	if files.Auth == nil {
		return fmt.Errorf("Codex login completed without creating auth.json")
	}
	info, identity, err := describe(*files.Auth, nil)
	if err != nil {
		return err
	}
	state, err := s.repo.Load()
	if err != nil {
		return err
	}
	for _, op := range state.Operations {
		if op.ID == attempt.view.ID && op.Action == "login" {
			attempt.view.AccountID = op.AccountID
			return nil
		}
	}
	now := time.Now().UnixMilli()
	info.ID, info.Name, info.Note = uuid.NewString(), attempt.view.Name, attempt.note
	info.CreatedAt, info.UpdatedAt = now, now
	state.Accounts = append(state.Accounts, repository.StoredCodexAccount{Info: info, Identity: identity, Auth: *files.Auth})
	state.Revision++
	appendOperation(state, model.CodexAccountOperation{ID: attempt.view.ID, Action: "login", AccountID: info.ID, Status: "completed"})
	if err := s.repo.Save(state); err != nil {
		return err
	}
	attempt.view.AccountID = info.ID
	return nil
}

func (s *Service) ReadLogin(id string) (model.CodexAccountLogin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if attempt := s.logins[id]; attempt != nil {
		return attempt.view, nil
	}
	return model.CodexAccountLogin{}, fmt.Errorf("Codex login %q does not exist; the backend may have restarted", id)
}

func (s *Service) CancelLogin(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt := s.logins[id]
	if attempt == nil {
		return fmt.Errorf("Codex login %q does not exist", id)
	}
	if attempt.cancel != nil {
		attempt.cancel()
	}
	return nil
}
