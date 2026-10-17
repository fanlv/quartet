package repository

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

type StoredCodexAccount struct {
	Info     model.CodexAccount `json:"info"`
	Identity string             `json:"identity"`
	Auth     string             `json:"auth"`
	Config   *string            `json:"config,omitempty"`
}

type CodexAccountFiles struct {
	Home   string  `json:"home"`
	Auth   *string `json:"auth"`
	Config *string `json:"config"`
}

func (f CodexAccountFiles) Revision() string {
	raw, _ := json.Marshal(f)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type CodexAccountBackup struct {
	Files         CodexAccountFiles `json:"files"`
	ManagedID     string            `json:"managed_id"`
	ConfigApplied bool              `json:"config_applied"`
}

type CodexAccountTransaction struct {
	Before    CodexAccountBackup          `json:"before"`
	After     CodexAccountFiles           `json:"after"`
	Operation model.CodexAccountOperation `json:"operation"`
}

type CodexAccountStore struct {
	Version         int                           `json:"version"`
	Revision        uint64                        `json:"revision"`
	Accounts        []StoredCodexAccount          `json:"accounts"`
	ActiveID        string                        `json:"active_id"`
	ActiveHome      string                        `json:"active_home"`
	ConfigApplied   bool                          `json:"config_applied"`
	Backup          *CodexAccountBackup           `json:"backup,omitempty"`
	Pending         *CodexAccountTransaction      `json:"pending,omitempty"`
	Operations      []model.CodexAccountOperation `json:"operations"`
	OperationHashes map[string]string             `json:"operation_hashes,omitempty"`
}

type CodexAccountRepo struct {
	file string
	mu   sync.Mutex
}

func NewCodexAccountRepo() (*CodexAccountRepo, error) {
	file, err := typepath.CodexAccountsFile()
	if err != nil {
		return nil, err
	}
	return &CodexAccountRepo{file: file}, nil
}

func (r *CodexAccountRepo) Load() (*CodexAccountStore, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := os.ReadFile(r.file)
	if errors.Is(err, os.ErrNotExist) {
		return &CodexAccountStore{Version: 1, Accounts: []StoredCodexAccount{}, Operations: []model.CodexAccountOperation{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Codex accounts %s: %w", r.file, err)
	}
	var state CodexAccountStore
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("parse Codex accounts %s: %w", r.file, err)
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("unsupported Codex account schema version %d in %s", state.Version, r.file)
	}
	if state.Accounts == nil {
		state.Accounts = []StoredCodexAccount{}
	}
	if state.Operations == nil {
		state.Operations = []model.CodexAccountOperation{}
	}
	return &state, nil
}

func (r *CodexAccountRepo) Save(state *CodexAccountStore) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(r.file), 0o700); err != nil {
		return fmt.Errorf("create Codex account directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(r.file), 0o700); err != nil {
		return fmt.Errorf("secure Codex account directory: %w", err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Codex accounts: %w", err)
	}
	if err := AtomicWriteFile(r.file, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("save Codex accounts: %w", err)
	}
	return nil
}

func (r *CodexAccountRepo) ReadFiles(home string) (CodexAccountFiles, error) {
	files := CodexAccountFiles{Home: home}
	for _, item := range []struct {
		name   string
		target **string
	}{{"auth.json", &files.Auth}, {"config.toml", &files.Config}} {
		raw, err := os.ReadFile(filepath.Join(home, item.name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return files, fmt.Errorf("read Codex %s: %w", item.name, err)
		}
		value := string(raw)
		*item.target = &value
	}
	return files, nil
}

// ReplaceFiles compares each file immediately before replacement. Restoration
// accepts either side of an interrupted transaction and refuses external edits.
func (r *CodexAccountRepo) ReplaceFiles(before, after CodexAccountFiles) error {
	if before.Home != after.Home || !filepath.IsAbs(before.Home) {
		return fmt.Errorf("invalid Codex transaction home")
	}
	if err := os.MkdirAll(after.Home, 0o700); err != nil {
		return fmt.Errorf("create Codex home %s: %w", after.Home, err)
	}
	for _, item := range []struct {
		name      string
		old, next *string
	}{{"auth.json", before.Auth, after.Auth}, {"config.toml", before.Config, after.Config}} {
		if sameCodexFile(item.old, item.next) {
			continue
		}
		path := filepath.Join(after.Home, item.name)
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read %s before replacement: %w", path, err)
		}
		if (item.old == nil && !errors.Is(err, os.ErrNotExist)) || (item.old != nil && (err != nil || !bytes.Equal(raw, []byte(*item.old)))) {
			return fmt.Errorf("Codex file changed externally: %s; replacement refused", path)
		}
		if item.next == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", path, err)
			}
		} else if err := AtomicWriteFile(path, []byte(*item.next), 0o600); err != nil {
			return fmt.Errorf("replace %s: %w", path, err)
		}
	}
	actual, err := r.ReadFiles(after.Home)
	if err != nil {
		return err
	}
	if actual.Revision() != after.Revision() {
		return fmt.Errorf("Codex files changed externally during replacement in %s", after.Home)
	}
	return nil
}

func (r *CodexAccountRepo) Rollback(transaction *CodexAccountTransaction) error {
	actual, err := r.ReadFiles(transaction.Before.Files.Home)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		name                  string
		actual, before, after *string
	}{{"auth.json", actual.Auth, transaction.Before.Files.Auth, transaction.After.Auth}, {"config.toml", actual.Config, transaction.Before.Files.Config, transaction.After.Config}} {
		if !sameCodexFile(item.actual, item.before) && !sameCodexFile(item.actual, item.after) {
			return fmt.Errorf("cannot restore Codex %s: external changes detected in %s", item.name, actual.Home)
		}
	}
	return r.ReplaceFiles(actual, transaction.Before.Files)
}

func sameCodexFile(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (r *CodexAccountRepo) PrepareLoginHome(id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	dir, err := typepath.CodexLoginTmpDir()
	if err != nil {
		return "", err
	}
	home := filepath.Join(dir, id)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("create isolated Codex login home: %w", err)
	}
	if err := AtomicWriteFile(filepath.Join(home, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\n"), 0o600); err != nil {
		return "", err
	}
	return home, nil
}

func (r *CodexAccountRepo) RemoveLoginHome(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	dir, err := typepath.CodexLoginTmpDir()
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(dir, id))
}
