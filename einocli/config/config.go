// Package config is eino-cli's self-managed configuration store. When
// LOCAL_MEMORY is available, the model catalog and general config live under
// the persistent configuration directory at quartet/config/eino/. Session state
// remains under Root() (~/.eino/ by default). The package has no dependency on
// quartet — the quartet backend talks to it exclusively through `eino-cli`
// subcommands.
//
// Files here hold API keys, so every write is atomic (temp + rename) with
// 0600 permissions, and Masked() exists for any JSON that leaves the
// process.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/einocli/modelbuilder"
	"github.com/google/uuid"
)

// Root returns the eino-cli session-state directory: $EINO_HOME if set, else
// ~/.eino. Model and system-prompt configuration use configRoot instead.
func Root() string {
	if v := os.Getenv("EINO_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to the current directory rather than crashing: an
		// ACP server with a broken HOME is better off logging in a
		// local dir than dying before the handshake.
		return ".eino"
	}
	return filepath.Join(home, ".eino")
}

// configRoot returns the persistent eino configuration directory when
// quartet supplies LOCAL_MEMORY. Standalone eino-cli usage without
// LOCAL_MEMORY keeps configuration alongside session state under Root().
func configRoot() string {
	if root := strings.TrimSpace(os.Getenv("LOCAL_MEMORY")); root != "" {
		return filepath.Join(filepath.Clean(root), "quartet", "config", "eino")
	}
	return Root()
}

// SessionsDir returns the directory holding per-session state.
func SessionsDir() string { return filepath.Join(Root(), "sessions") }

// ModelsFile returns the model catalog file path.
func ModelsFile() string { return filepath.Join(configRoot(), "models.json") }

// ConfigFile returns the general config file path.
func ConfigFile() string { return filepath.Join(configRoot(), "config.json") }

// Model is one entry in the model catalog.
type Model struct {
	ID           string                       `json:"id"`
	ModelClass   modelbuilder.ModelClass      `json:"model_class"`
	DisplayName  string                       `json:"display_name"`
	Connection   *modelbuilder.ConnectionInfo `json:"connection"`
	ThinkingType modelbuilder.ThinkingType    `json:"thinking_type,omitempty"`
	CreatedAt    int64                        `json:"created_at"`
	UpdatedAt    int64                        `json:"updated_at"`
}

// ToModelConfig maps a catalog entry to a modelbuilder.ModelConfig. A non-empty
// thinkingOverride wins over the model's own ThinkingType (used for the
// session-level thought_level override); pass "" for no override.
func (m *Model) ToModelConfig(thinkingOverride string) *modelbuilder.ModelConfig {
	thinking := m.ThinkingType
	if thinkingOverride != "" {
		thinking = modelbuilder.ThinkingType(thinkingOverride)
	}
	return &modelbuilder.ModelConfig{
		ModelClass:   m.ModelClass,
		Connection:   m.Connection,
		ThinkingType: thinking,
	}
}

// mu serialises all config reads and writes within the process. Load+save
// cycles (AddModel/DeleteModel/SetSystemPrompt) hold it across the whole
// read-modify-write so concurrent callers can't lose updates.
var mu sync.Mutex

func validateModel(m *Model) error {
	if m == nil {
		return errors.New("model is nil")
	}
	if !modelbuilder.IsSupportedClass(m.ModelClass) {
		return fmt.Errorf("model class %q not supported", m.ModelClass)
	}
	if m.DisplayName == "" {
		return errors.New("display name is empty")
	}
	if m.Connection == nil {
		return errors.New("connection is nil")
	}
	if m.Connection.Model == "" {
		return errors.New("connection model is empty")
	}
	return nil
}

// loadModelsLocked reads models.json. A missing file yields an empty list,
// not an error. Caller must hold mu.
func loadModelsLocked() ([]*Model, error) {
	path := ModelsFile()
	if err := migrateLegacyConfigFileLocked(path); err != nil {
		return nil, fmt.Errorf("migrate models file failed: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read models file failed: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var models []*Model
	if err := json.Unmarshal(data, &models); err != nil {
		return nil, fmt.Errorf("unmarshal models file failed: %w", err)
	}
	return models, nil
}

// migrateLegacyConfigFileLocked copies one config file from the former
// Root()-based store when the persistent destination does not exist yet.
// The source is deliberately retained so migration is recoverable.
// Caller must hold mu.
func migrateLegacyConfigFileLocked(destination string) error {
	source := filepath.Join(Root(), filepath.Base(destination))
	if filepath.Clean(source) == filepath.Clean(destination) {
		return nil
	}
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat destination %q failed: %w", destination, err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read legacy config %q failed: %w", source, err)
	}
	if err := atomicWrite(destination, data); err != nil {
		return fmt.Errorf("copy legacy config %q to %q failed: %w", source, destination, err)
	}
	return nil
}

// atomicWrite writes data to path atomically (temp file + rename) with 0600
// permissions — the files here hold API keys. Parent dirs are created.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %q: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %q: %w", path, err)
	}
	return nil
}

// ListModels returns every model in the catalog. A missing models.json is
// an empty list, not an error.
func ListModels() ([]*Model, error) {
	mu.Lock()
	defer mu.Unlock()
	models, err := loadModelsLocked()
	if err != nil {
		return nil, err
	}
	if models == nil {
		models = []*Model{}
	}
	return models, nil
}

// GetModel returns the model with the given ID.
func GetModel(id string) (*Model, error) {
	mu.Lock()
	defer mu.Unlock()
	models, err := loadModelsLocked()
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		if m.ID == id {
			return m, nil
		}
	}
	return nil, fmt.Errorf("model %q not found", id)
}

// AddModel validates m, assigns a short-uuid ID when empty, and upserts by
// ID. On update the original CreatedAt is preserved and UpdatedAt is
// refreshed. Returns the stored model.
func AddModel(m *Model) (*Model, error) {
	if err := validateModel(m); err != nil {
		return nil, err
	}

	mu.Lock()
	defer mu.Unlock()

	models, err := loadModelsLocked()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	if m.ID == "" {
		m.ID = uuid.NewString()[:8]
	}

	stored := *m
	for i, existing := range models {
		if existing.ID == m.ID {
			stored.CreatedAt = existing.CreatedAt
			stored.UpdatedAt = now
			models[i] = &stored
			return &stored, saveModelsLocked(models)
		}
	}

	stored.CreatedAt = now
	stored.UpdatedAt = now
	models = append(models, &stored)
	return &stored, saveModelsLocked(models)
}

func saveModelsLocked(models []*Model) error {
	data, err := json.MarshalIndent(models, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal models failed: %w", err)
	}
	if err := atomicWrite(ModelsFile(), data); err != nil {
		return fmt.Errorf("write models file failed: %w", err)
	}
	return nil
}

// DeleteModel removes the model with the given ID.
func DeleteModel(id string) error {
	mu.Lock()
	defer mu.Unlock()

	models, err := loadModelsLocked()
	if err != nil {
		return err
	}
	for i, m := range models {
		if m.ID == id {
			models = append(models[:i], models[i+1:]...)
			return saveModelsLocked(models)
		}
	}
	return fmt.Errorf("model %q not found", id)
}

// appConfig is the shape of config.json.
type appConfig struct {
	SystemPrompt string `json:"system_prompt"`
}

func loadConfigLocked() (*appConfig, error) {
	path := ConfigFile()
	if err := migrateLegacyConfigFileLocked(path); err != nil {
		return nil, fmt.Errorf("migrate config file failed: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &appConfig{}, nil
		}
		return nil, fmt.Errorf("read config file failed: %w", err)
	}
	if len(data) == 0 {
		return &appConfig{}, nil
	}
	var cfg appConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config file failed: %w", err)
	}
	return &cfg, nil
}

// GetSystemPrompt returns the configured system prompt ("" when unset).
func GetSystemPrompt() (string, error) {
	mu.Lock()
	defer mu.Unlock()
	cfg, err := loadConfigLocked()
	if err != nil {
		return "", err
	}
	return cfg.SystemPrompt, nil
}

// SetSystemPrompt persists the system prompt to config.json.
func SetSystemPrompt(prompt string) error {
	mu.Lock()
	defer mu.Unlock()
	cfg, err := loadConfigLocked()
	if err != nil {
		return err
	}
	cfg.SystemPrompt = prompt
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config failed: %w", err)
	}
	if err := atomicWrite(ConfigFile(), data); err != nil {
		return fmt.Errorf("write config file failed: %w", err)
	}
	return nil
}

// Masked returns a copy of m with Connection.APIKey replaced by "***" when
// non-empty. Use it for any model JSON that leaves the process (CLI output,
// ACP responses) so keys never cross the boundary.
func Masked(m *Model) *Model {
	if m == nil {
		return nil
	}
	out := *m
	if m.Connection != nil {
		conn := *m.Connection
		if conn.APIKey != "" {
			conn.APIKey = "***"
		}
		out.Connection = &conn
	}
	return &out
}
