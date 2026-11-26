package codexaccounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/catalog"
	"github.com/fanlv/quartet/services/agent/runtimeenv"
	"github.com/fanlv/quartet/types/consts"
	"github.com/google/uuid"
)

func accessTokenExpired(raw string) bool {
	var auth codexAuthFile
	if err := json.Unmarshal([]byte(raw), &auth); err != nil || auth.Tokens == nil || strings.TrimSpace(auth.APIKey) != "" {
		return false
	}
	var claims struct {
		ExpiresAt *int64 `json:"exp"`
	}
	return decodeClaims(auth.Tokens.Access, &claims) == nil && claims.ExpiresAt != nil && *claims.ExpiresAt <= time.Now().Unix()
}

func (s *Service) prepareExpiredAccount(ctx context.Context, state *repository.CodexAccountStore, index int, live repository.CodexAccountFiles, target *repository.CodexAccountFiles, env map[string]string) error {
	account := &state.Accounts[index]
	if sameText(live.Auth, target.Auth) {
		return nil
	}
	// Refreshing the active grant in a temporary directory could consume the
	// live refresh token. Keep the live file when both snapshots share it.
	if live.Auth != nil && refreshToken(*live.Auth) == refreshToken(*target.Auth) {
		if _, identity, err := describe(*live.Auth, nil); err == nil && identity == account.Identity {
			if !olderCredentials(*live.Auth, account.Auth) {
				s.replaceAccountAuth(state, index, *live.Auth)
			}
			target.Auth = live.Auth
			return nil
		}
	}
	if err := s.refreshStoredAccount(ctx, state, index, *target, env); err != nil {
		return fmt.Errorf("refresh expired Codex account %q (%s) before switching: %w", account.Info.Name, account.Info.ID, err)
	}
	target.Auth = &account.Auth
	return nil
}

func (s *Service) refreshStoredAccount(ctx context.Context, state *repository.CodexAccountStore, index int, target repository.CodexAccountFiles, env map[string]string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := uuid.NewString()
	home, err := s.repo.PrepareLoginHome(id)
	if err != nil {
		return err
	}
	keepHome := false
	defer func() {
		if !keepHome {
			if cleanupErr := s.repo.RemoveLoginHome(id); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove temporary Codex refresh directory: %w", cleanupErr))
			}
		}
	}()
	before, err := s.repo.ReadFiles(home)
	if err != nil {
		return err
	}
	staged := repository.CodexAccountFiles{Home: home, Auth: target.Auth, Config: target.Config}
	if err := s.repo.ReplaceFiles(before, staged); err != nil {
		return err
	}

	// Finish saving a possible rotation even if the account-switch request disconnects.
	stderr, refreshErr := refreshWithCodex(context.WithoutCancel(ctx), home, env)
	defer func() {
		if err != nil && stderr != "" {
			err = fmt.Errorf("%w\nCodex app-server stderr: %s", err, stderr)
		}
	}()
	refreshed, err := s.repo.ReadFiles(home)
	if err != nil {
		keepHome = true
		return errors.Join(refreshErr, fmt.Errorf("read refreshed Codex files retained in %s: %w", home, err))
	}
	if refreshed.Auth == nil || sameText(staged.Auth, refreshed.Auth) || accessTokenExpired(*refreshed.Auth) {
		return errors.Join(refreshErr, fmt.Errorf("Codex did not write an updated login with a valid access token"))
	}
	info, identity, err := describe(*refreshed.Auth, nil)
	if err != nil {
		return errors.Join(refreshErr, err)
	}
	account := &state.Accounts[index]
	if identity != account.Identity {
		return errors.Join(refreshErr, fmt.Errorf("Codex refreshed a different user or workspace"))
	}
	s.replaceAccountAuth(state, index, *refreshed.Auth)
	account.Info.Email = info.Email
	state.Revision++
	// A later replacement failure must not discard the newly issued token.
	if err := s.repo.Save(state); err != nil {
		keepHome = true
		return errors.Join(refreshErr, fmt.Errorf("save refreshed Codex credentials; updated auth.json retained in %s: %w", home, err))
	}
	return refreshErr
}

func refreshWithCodex(ctx context.Context, home string, environment map[string]string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	adapter, ok := catalog.FindBuiltinByID("codex")
	if !ok {
		return "", fmt.Errorf("Codex Agent does not exist")
	}
	// The adapter's CLI forwarding selects the same bundled or configured
	// executable as the ACP runtime. Codex owns OAuth refresh and file writes.
	cmd := executil.CommandContext(ctx, adapter.ACPProgram, "cli", "app-server", "-c", `cli_auth_credentials_store="file"`)
	executil.ConfigureProcessTreeCancellation(cmd)
	cmd.Dir = home
	cmd.Env = os.Environ()
	for key, value := range (runtimeenv.Resolver{}).Effective(adapter.AgentID, environment) {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Env = append(cmd.Env, consts.EnvKeyCodexHome+"="+home)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("open Codex app-server stdin: %w", err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("open Codex app-server stdout: %w", err)
	}
	defer stdout.Close()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start Codex app-server: %w", err)
	}
	checkErr := requestCodexRefresh(stdin, stdout)
	closeErr := stdin.Close()
	_, drainErr := io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	return stderr.String(), errors.Join(checkErr, closeErr, drainErr, waitErr, ctx.Err())
}

func requestCodexRefresh(stdin io.Writer, stdout io.Reader) error {
	enc, dec := json.NewEncoder(stdin), json.NewDecoder(stdout)
	type request struct {
		Method string `json:"method"`
		ID     int    `json:"id,omitempty"`
		Params any    `json:"params,omitempty"`
	}
	call := func(req request) error {
		if err := enc.Encode(req); err != nil {
			return fmt.Errorf("send Codex %s: %w", req.Method, err)
		}
		for {
			var response struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := dec.Decode(&response); err != nil {
				return fmt.Errorf("read Codex %s response: %w", req.Method, err)
			}
			if string(response.ID) != strconv.Itoa(req.ID) {
				continue
			}
			if len(response.Error) > 0 && string(response.Error) != "null" {
				return fmt.Errorf("Codex %s RPC error: %s", req.Method, response.Error)
			}
			if len(response.Result) == 0 {
				return fmt.Errorf("Codex %s response has no result", req.Method)
			}
			return nil
		}
	}
	initParams := map[string]any{"clientInfo": map[string]string{"name": "quartet", "version": "1"}}
	if err := call(request{Method: "initialize", ID: 1, Params: initParams}); err != nil {
		return err
	}
	if err := enc.Encode(request{Method: "initialized"}); err != nil {
		return fmt.Errorf("send Codex initialized notification: %w", err)
	}
	// getAuthStatus can refresh without waiting for workspace discovery. The
	// caller checks auth.json because a status response can use cached auth.
	return call(request{Method: "getAuthStatus", ID: 2, Params: map[string]bool{"refreshToken": true, "includeToken": false}})
}
