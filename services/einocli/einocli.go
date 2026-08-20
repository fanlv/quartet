// Package einocli wraps exec calls into the standalone eino-cli binary, which
// owns the eino model catalog (including API keys) and the eino system prompt
// under LOCAL_MEMORY/quartet/config/eino/. quartet never parses or persists
// eino model configs itself — it only shells out to `eino-cli models ...` /
// `eino-cli systemprompt ...` and passes JSON through, so secrets never leave
// the eino-cli process's own storage.
package einocli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// execTimeout bounds a single eino-cli subcommand call. These commands are
// local JSON file reads/writes, so this is generous.
const execTimeout = 20 * time.Second

// Service execs the eino-cli binary for model-catalog and system-prompt IO.
type Service struct {
	mu  sync.Mutex // guards the lazy bin re-resolution below
	bin string     // resolved at construction; re-resolved per call when empty
}

// NewService resolves the eino-cli binary on $PATH. A missing binary is not
// fatal here — every method retries the lookup and returns a clear error
// until `make build-eino-cli` (or equivalent) installs it.
func NewService() *Service {
	bin, _ := exec.LookPath("eino-cli")
	return &Service{bin: bin}
}

func (s *Service) resolveBin() (string, error) {
	// Handlers hit this concurrently and it may write s.bin, so the whole
	// read-check-write runs under the lock. LookPath is idempotent, so the
	// worst a second caller pays is a redundant lookup, never a torn string.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bin != "" {
		return s.bin, nil
	}
	bin, err := exec.LookPath("eino-cli")
	if err != nil {
		return "", fmt.Errorf("eino-cli binary not found on $PATH; install it with `make build-eino-cli`: %w", err)
	}
	s.bin = bin
	return bin, nil
}

// run executes `eino-cli <args...>` with stdinJSON piped on stdin (when
// non-empty) and decodes the single JSON document on stdout into out (when
// non-nil). The full stderr tail is included in the returned error — error
// text is user-visible and must not be truncated away.
func (s *Service) run(ctx context.Context, args []string, stdinJSON string, out any) error {
	bin, err := s.resolveBin()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdinJSON != "" {
		cmd.Stdin = strings.NewReader(stdinJSON)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	logger.Infof(ctx, "[einocli] exec: %s %s", bin, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText == "" {
			errText = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("eino-cli %s failed: %v: %s", strings.Join(args, " "), err, errText)
	}
	if out != nil {
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), out); err != nil {
			return fmt.Errorf("eino-cli %s returned invalid JSON: %v: %s", strings.Join(args, " "), err, stdout.String())
		}
	}
	return nil
}

// ListModels returns the eino-cli model catalog (API keys already masked by
// eino-cli itself).
func (s *Service) ListModels(ctx context.Context) ([]*model.EinoModel, error) {
	var models []*model.EinoModel
	if err := s.run(ctx, []string{"models", "list"}, "", &models); err != nil {
		return nil, err
	}
	if models == nil {
		models = []*model.EinoModel{}
	}
	return models, nil
}

// AddModel writes a new model config (including its API key) into eino-cli's
// own storage via stdin JSON and returns the created (masked) model.
func (s *Service) AddModel(ctx context.Context, in *model.CreateEinoModelRequest) (*model.EinoModel, error) {
	payload, err := json.Marshal(map[string]any{
		"model_class":   in.ModelClass,
		"display_name":  in.DisplayName,
		"connection":    in.Connection,
		"thinking_type": in.ThinkingType,
	})
	if err != nil {
		return nil, err
	}
	var created model.EinoModel
	if err := s.run(ctx, []string{"models", "add"}, string(payload), &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// DeleteModel removes a model from the eino-cli catalog.
func (s *Service) DeleteModel(ctx context.Context, id string) error {
	return s.run(ctx, []string{"models", "delete", "--id", id}, "", nil)
}

// GetSystemPrompt reads eino-cli's configured system prompt.
func (s *Service) GetSystemPrompt(ctx context.Context) (string, error) {
	var resp struct {
		SystemPrompt string `json:"system_prompt"`
	}
	if err := s.run(ctx, []string{"systemprompt", "get"}, "", &resp); err != nil {
		return "", err
	}
	return resp.SystemPrompt, nil
}

// SetSystemPrompt writes eino-cli's system prompt. The payload goes via the
// --json flag: stdin without the flag is treated as raw prompt text.
func (s *Service) SetSystemPrompt(ctx context.Context, prompt string) error {
	payload, err := json.Marshal(map[string]string{"system_prompt": prompt})
	if err != nil {
		return err
	}
	return s.run(ctx, []string{"systemprompt", "set", "--json", string(payload)}, "", nil)
}
