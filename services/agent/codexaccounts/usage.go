package codexaccounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/usage"
	"github.com/fanlv/quartet/types/model"
	"github.com/google/uuid"
)

var ErrInvalidReset = errors.New("invalid Codex reset request")

func (s *Service) CurrentUsage(ctx context.Context, liveRevision string) (*model.CodexAccountUsageResponse, error) {
	s.mu.Lock()
	client, err := s.currentUsageClient(liveRevision)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	result, err := client.Usage(ctx)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.currentUsageFiles(liveRevision); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) SavedUsage(ctx context.Context, accountID, revision string) (*model.CodexAccountUsageResponse, error) {
	state, err := s.repo.Load()
	if err != nil {
		return nil, err
	}
	if revision != fmt.Sprint(state.Revision) {
		return nil, fmt.Errorf("%w: saved Codex accounts changed; refresh and try again", ErrConflict)
	}
	index := slices.IndexFunc(state.Accounts, func(account repository.StoredCodexAccount) bool {
		return account.Info.ID == accountID
	})
	if index < 0 {
		return nil, fmt.Errorf("%w: Codex account %q does not exist", ErrConflict, accountID)
	}
	account := state.Accounts[index]
	if accessTokenExpired(account.Auth) {
		return nil, fmt.Errorf("%w: the saved login for %q has expired; switch to the account to refresh it or update its auth.json", ErrConflict, account.Info.Name)
	}
	_, env, err := s.home()
	if err != nil {
		return nil, err
	}
	client, err := subscriptionUsageClient(account.Auth, env)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	quota, err := client.Quota(ctx)
	if err != nil {
		return nil, err
	}
	return &model.CodexAccountUsageResponse{Usage: quota}, nil
}

func (s *Service) RedeemReset(ctx context.Context, request model.CodexAccountResetRequest) (model.CodexAccountResetResponse, error) {
	if strings.TrimSpace(request.CreditID) == "" {
		return model.CodexAccountResetResponse{}, fmt.Errorf("%w: credit_id is required", ErrInvalidReset)
	}
	operationID, err := uuid.Parse(request.OperationID)
	if err != nil {
		return model.CodexAccountResetResponse{}, fmt.Errorf("%w: operation_id must be a UUID: %v", ErrInvalidReset, err)
	}
	client, err := s.beginReset(request.LiveRevision)
	if err != nil {
		return model.CodexAccountResetResponse{}, err
	}
	defer func() {
		s.mu.Lock()
		s.resetInProgress = false
		s.mu.Unlock()
	}()
	defer client.Close()

	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cards, count, err := client.ResetCards(ctx)
	if err != nil {
		return model.CodexAccountResetResponse{}, err
	}
	if !slices.ContainsFunc(cards, func(card model.CodexResetCard) bool { return card.ID == request.CreditID }) {
		outcome := "credit_unavailable"
		if count == 0 {
			outcome = "no_credit"
		}
		return model.CodexAccountResetResponse{Outcome: outcome}, nil
	}

	// Recheck after the provider read. External Codex or environment changes
	// can replace the active files even while Quartet's mutations are blocked.
	if _, _, err := s.currentUsageFiles(request.LiveRevision); err != nil {
		return model.CodexAccountResetResponse{}, err
	}
	return client.RedeemReset(ctx, request.CreditID, operationID.String())
}

func (s *Service) beginReset(liveRevision string) (*usage.CodexClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resetInProgress {
		return nil, fmt.Errorf("%w: a Codex reset card is already being redeemed", ErrConflict)
	}
	client, err := s.currentUsageClient(liveRevision)
	if err != nil {
		return nil, err
	}
	s.resetInProgress = true
	return client, nil
}

func (s *Service) currentUsageClient(liveRevision string) (*usage.CodexClient, error) {
	files, env, err := s.currentUsageFiles(liveRevision)
	if err != nil {
		return nil, err
	}
	if files.Auth == nil {
		return nil, fmt.Errorf("%w: no current Codex login", ErrConflict)
	}
	return subscriptionUsageClient(*files.Auth, env)
}

func subscriptionUsageClient(raw string, env map[string]string) (*usage.CodexClient, error) {
	info, _, err := describe(raw, nil)
	if err != nil {
		return nil, err
	}
	if info.AuthType != "chatgpt" {
		return nil, fmt.Errorf("%w: Codex subscription usage requires a ChatGPT login", ErrConflict)
	}
	var auth codexAuthFile
	if err := json.Unmarshal([]byte(raw), &auth); err != nil {
		return nil, fmt.Errorf("parse Codex auth.json: %w", err)
	}
	return usage.NewCodexClient(env, auth.Tokens.Access, info.WorkspaceID), nil
}

func (s *Service) currentUsageFiles(liveRevision string) (repository.CodexAccountFiles, map[string]string, error) {
	var files repository.CodexAccountFiles
	state, err := s.repo.Load()
	if err != nil {
		return files, nil, err
	}
	if state.Pending != nil {
		return files, nil, fmt.Errorf("%w: interrupted replacement requires recovery", ErrConflict)
	}
	home, env, err := s.home()
	if err != nil {
		return files, nil, err
	}
	files, err = s.repo.ReadFiles(home)
	if err != nil {
		return files, nil, err
	}
	if files.Revision() != liveRevision {
		return files, nil, fmt.Errorf("%w: local Codex files changed; refresh the account and try again", ErrConflict)
	}
	if err := applicationConflict(files.Config, env); err != nil {
		return files, nil, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return files, env, nil
}
