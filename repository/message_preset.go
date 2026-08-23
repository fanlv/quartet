package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

const missingMessagePresetRevision = "missing"

var ErrMessagePresetConflict = errors.New("message preset configuration conflict")

type StoredMessagePresetConfig struct {
	Config   *model.MessagePresetConfig
	Revision string
	File     string
}

type MessagePresetRepo interface {
	GetGlobal() (*StoredMessagePresetConfig, error)
	GetWorkspace(workspaceID string) (*StoredMessagePresetConfig, error)
	ListWorkspaceFiles() ([]string, error)
	SaveGlobal(expectedRevision string, config *model.MessagePresetConfig) (*StoredMessagePresetConfig, error)
	SaveWorkspace(workspaceID, expectedRevision string, config *model.MessagePresetConfig) (*StoredMessagePresetConfig, error)
	DeleteWorkspace(workspaceID, expectedRevision string) error
	RebindWorkspace(sourceID, targetID, expectedRevision string, config *model.MessagePresetConfig) error
}

type fileMessagePresetRepo struct {
	globalFile    string
	workspacesDir string
	mu            sync.Mutex
}

func NewMessagePresetRepo() (MessagePresetRepo, error) {
	globalFile, err := typepath.GlobalMessagePresetsFile()
	if err != nil {
		return nil, fmt.Errorf("resolve global message presets file: %w", err)
	}
	workspacesDir, err := typepath.WorkspaceMessagePresetsDir()
	if err != nil {
		return nil, fmt.Errorf("resolve workspace message presets directory: %w", err)
	}
	return &fileMessagePresetRepo{globalFile: globalFile, workspacesDir: workspacesDir}, nil
}

func messagePresetRevision(raw []byte) string {
	if raw == nil {
		return missingMessagePresetRevision
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readMessagePresetConfig(file string) (*StoredMessagePresetConfig, error) {
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return &StoredMessagePresetConfig{
			Config:   &model.MessagePresetConfig{SchemaVersion: model.MessagePresetSchemaVersion, Messages: []model.MessagePreset{}},
			Revision: missingMessagePresetRevision,
			File:     file,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read message preset configuration %s: %w", file, err)
	}
	var config model.MessagePresetConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("parse message preset configuration %s: %w", file, err)
	}
	if config.Messages == nil {
		config.Messages = []model.MessagePreset{}
	}
	return &StoredMessagePresetConfig{Config: &config, Revision: messagePresetRevision(raw), File: file}, nil
}

func marshalMessagePresetConfig(file string, config *model.MessagePresetConfig) ([]byte, error) {
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal message preset configuration %s: %w", file, err)
	}
	return append(raw, '\n'), nil
}

func checkMessagePresetRevision(file, expected string) error {
	current, err := readMessagePresetConfig(file)
	if err != nil {
		return err
	}
	if expected == "" || current.Revision != expected {
		return fmt.Errorf("%w: file=%s expected revision=%q current revision=%q", ErrMessagePresetConflict, file, expected, current.Revision)
	}
	return nil
}

func saveMessagePresetConfig(file, expected string, config *model.MessagePresetConfig) (*StoredMessagePresetConfig, error) {
	if err := checkMessagePresetRevision(file, expected); err != nil {
		return nil, err
	}
	if len(config.Messages) == 0 {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove empty message preset configuration %s: %w", file, err)
		}
		return readMessagePresetConfig(file)
	}
	raw, err := marshalMessagePresetConfig(file, config)
	if err != nil {
		return nil, err
	}
	if err := AtomicWriteFile(file, raw, 0o644); err != nil {
		return nil, fmt.Errorf("write message preset configuration %s: %w", file, err)
	}
	return &StoredMessagePresetConfig{Config: config, Revision: messagePresetRevision(raw), File: file}, nil
}

func (r *fileMessagePresetRepo) GetGlobal() (*StoredMessagePresetConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return readMessagePresetConfig(r.globalFile)
}

func (r *fileMessagePresetRepo) GetWorkspace(workspaceID string) (*StoredMessagePresetConfig, error) {
	if err := validateID(workspaceID); err != nil {
		return nil, fmt.Errorf("invalid workspace id: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return readMessagePresetConfig(filepath.Join(r.workspacesDir, workspaceID+".json"))
}

func (r *fileMessagePresetRepo) ListWorkspaceFiles() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := os.ReadDir(r.workspacesDir)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list workspace message preset configurations %s: %w", r.workspacesDir, err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		files = append(files, filepath.Join(r.workspacesDir, entry.Name()))
	}
	sort.Strings(files)
	return files, nil
}

func (r *fileMessagePresetRepo) SaveGlobal(expectedRevision string, config *model.MessagePresetConfig) (*StoredMessagePresetConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return saveMessagePresetConfig(r.globalFile, expectedRevision, config)
}

func (r *fileMessagePresetRepo) SaveWorkspace(workspaceID, expectedRevision string, config *model.MessagePresetConfig) (*StoredMessagePresetConfig, error) {
	if err := validateID(workspaceID); err != nil {
		return nil, fmt.Errorf("invalid workspace id: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return saveMessagePresetConfig(filepath.Join(r.workspacesDir, workspaceID+".json"), expectedRevision, config)
}

func (r *fileMessagePresetRepo) DeleteWorkspace(workspaceID, expectedRevision string) error {
	if err := validateID(workspaceID); err != nil {
		return fmt.Errorf("invalid workspace id: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	file := filepath.Join(r.workspacesDir, workspaceID+".json")
	if err := checkMessagePresetRevision(file, expectedRevision); err != nil {
		return err
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove message preset configuration %s: %w", file, err)
	}
	return nil
}

func (r *fileMessagePresetRepo) RebindWorkspace(sourceID, targetID, expectedRevision string, config *model.MessagePresetConfig) error {
	if err := validateID(sourceID); err != nil {
		return fmt.Errorf("invalid source workspace id: %w", err)
	}
	if err := validateID(targetID); err != nil {
		return fmt.Errorf("invalid target workspace id: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sourceFile := filepath.Join(r.workspacesDir, sourceID+".json")
	targetFile := filepath.Join(r.workspacesDir, targetID+".json")
	if err := checkMessagePresetRevision(sourceFile, expectedRevision); err != nil {
		return err
	}
	target, err := readMessagePresetConfig(targetFile)
	if err != nil {
		return err
	}
	if target.Revision != missingMessagePresetRevision {
		return fmt.Errorf("%w: target workspace %q already has message presets in %s", ErrMessagePresetConflict, targetID, targetFile)
	}
	raw, err := marshalMessagePresetConfig(targetFile, config)
	if err != nil {
		return err
	}
	if err := AtomicWriteFile(targetFile, raw, 0o644); err != nil {
		return fmt.Errorf("write rebound message preset configuration %s: %w", targetFile, err)
	}
	if err := os.Remove(sourceFile); err != nil {
		rollbackErr := os.Remove(targetFile)
		if rollbackErr != nil && !errors.Is(rollbackErr, os.ErrNotExist) {
			return fmt.Errorf("remove rebound source %s failed: %v; rollback target %s also failed: %w", sourceFile, err, targetFile, rollbackErr)
		}
		return fmt.Errorf("remove rebound source %s failed and target was rolled back: %w", sourceFile, err)
	}
	return nil
}
