package repository

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

type AuthRepo struct {
	systemFile  string
	usersDir    string
	rolesDir    string
	sessionsDir string
}

func NewAuthRepo() (*AuthRepo, error) {
	systemFile, err := typepath.AuthSystemFile()
	if err != nil {
		return nil, err
	}
	usersDir, err := typepath.AuthUsersDir()
	if err != nil {
		return nil, err
	}
	rolesDir, err := typepath.AuthRolesDir()
	if err != nil {
		return nil, err
	}
	sessionsDir, err := typepath.AuthSessionsDir()
	if err != nil {
		return nil, err
	}
	processNamespace := make([]byte, 16)
	if _, err := rand.Read(processNamespace); err != nil {
		return nil, fmt.Errorf("generate authentication session namespace: %w", err)
	}
	sessionsDir = filepath.Join(sessionsDir, hex.EncodeToString(processNamespace))
	return &AuthRepo{systemFile: systemFile, usersDir: usersDir, rolesDir: rolesDir, sessionsDir: sessionsDir}, nil
}

func readJSONFile[T any](path string) (*T, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &value, nil
}

func writeJSONFile(path string, value any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	raw = append(raw, '\n')
	return AtomicWriteFile(path, raw, mode)
}

func listJSONFiles[T any](dir string) ([]*T, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]*T, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		value, err := readJSONFile[T](filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (r *AuthRepo) LoadSystem() (*model.AuthSystem, error) {
	return readJSONFile[model.AuthSystem](r.systemFile)
}
func (r *AuthRepo) SaveSystem(value *model.AuthSystem) error {
	return writeJSONFile(r.systemFile, value, 0o600)
}
func (r *AuthRepo) ListUsers() ([]*model.User, error) { return listJSONFiles[model.User](r.usersDir) }
func (r *AuthRepo) SaveUser(value *model.User) error {
	return writeJSONFile(filepath.Join(r.usersDir, value.ID+".json"), value, 0o600)
}
func (r *AuthRepo) ListRoles() ([]*model.Role, error) { return listJSONFiles[model.Role](r.rolesDir) }
func (r *AuthRepo) SaveRole(value *model.Role) error {
	return writeJSONFile(filepath.Join(r.rolesDir, value.ID+".json"), value, 0o600)
}
func (r *AuthRepo) DeleteRole(id string) error {
	if strings.ContainsAny(id, `/\`) || id == "" {
		return fmt.Errorf("invalid role id %q", id)
	}
	err := os.Remove(filepath.Join(r.rolesDir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (r *AuthRepo) SaveSession(value *model.AuthSession) error {
	return writeJSONFile(filepath.Join(r.sessionsDir, value.TokenHash+".json"), value, 0o600)
}
func (r *AuthRepo) LoadSession(hash string) (*model.AuthSession, error) {
	return readJSONFile[model.AuthSession](filepath.Join(r.sessionsDir, hash+".json"))
}
func (r *AuthRepo) DeleteSession(hash string) error {
	err := os.Remove(filepath.Join(r.sessionsDir, hash+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (r *AuthRepo) ListSessions() ([]*model.AuthSession, error) {
	return listJSONFiles[model.AuthSession](r.sessionsDir)
}
func (r *AuthRepo) ConfigEntriesExist() (bool, error) {
	if _, err := os.Stat(r.systemFile); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, dir := range []string{r.usersDir, r.rolesDir} {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) > 0 {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func SortUsers(users []*model.User) {
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
}
func SortRoles(roles []*model.Role) {
	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
}
