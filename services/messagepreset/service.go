package messagepreset

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
	"github.com/google/uuid"
)

const (
	maxMessagesPerScope = 100
	maxContentBytes     = 32 * 1024
	maxNameRunes        = 80
)

var (
	ErrConflict   = repository.ErrMessagePresetConflict
	ErrNotFound   = errors.New("message preset workspace not found")
	ErrValidation = errors.New("invalid message preset configuration")
)

type Service interface {
	GetGlobal() (*model.MessagePresetScopeResponse, error)
	GetWorkspace(workspaceID string) (*model.MessagePresetScopeResponse, error)
	GetEffective(workspaceID string) (*model.EffectiveMessagePresetsResponse, error)
	SaveGlobal(req model.SaveMessagePresetScopeRequest) (*model.MessagePresetScopeResponse, error)
	SaveWorkspace(workspaceID string, req model.SaveMessagePresetScopeRequest) (*model.MessagePresetScopeResponse, error)
	ListOrphans() (*model.ListOrphanMessagePresetsResponse, error)
	DeleteOrphan(workspaceID, revision string) error
	RebindOrphan(sourceWorkspaceID string, req model.RebindMessagePresetRequest) error
}

type serviceImpl struct {
	repo       repository.MessagePresetRepo
	workspaces workspaceLookup
}

type workspaceLookup interface {
	Get(id string) (*model.Workspace, bool)
}

func NewService(workspaces workspaceLookup) (Service, error) {
	repo, err := repository.NewMessagePresetRepo()
	if err != nil {
		return nil, err
	}
	return &serviceImpl{repo: repo, workspaces: workspaces}, nil
}

func cloneMessages(items []model.MessagePreset) []model.MessagePreset {
	if len(items) == 0 {
		return []model.MessagePreset{}
	}
	return append([]model.MessagePreset(nil), items...)
}

func responseFromStored(stored *repository.StoredMessagePresetConfig) *model.MessagePresetScopeResponse {
	config := *stored.Config
	config.Messages = cloneMessages(stored.Config.Messages)
	return &model.MessagePresetScopeResponse{Code: 0, Revision: stored.Revision, Config: config}
}

func validateAndNormalize(config *model.MessagePresetConfig, global, generateIDs bool) error {
	if config == nil {
		return fmt.Errorf("%w: configuration is required", ErrValidation)
	}
	if config.SchemaVersion != model.MessagePresetSchemaVersion {
		return fmt.Errorf("%w: unsupported schemaVersion %d, expected %d", ErrValidation, config.SchemaVersion, model.MessagePresetSchemaVersion)
	}
	if global && (config.WorkspaceID != "" || config.WorkspaceTitle != "" || config.WorkspaceWorkdir != "") {
		return fmt.Errorf("%w: global configuration must not contain workspace metadata", ErrValidation)
	}
	if len(config.Messages) > maxMessagesPerScope {
		return fmt.Errorf("%w: message count %d exceeds limit %d", ErrValidation, len(config.Messages), maxMessagesPerScope)
	}
	seen := make(map[string]struct{}, len(config.Messages))
	for i := range config.Messages {
		item := &config.Messages[i]
		if !utf8.ValidString(item.Name) || !utf8.ValidString(item.Content) {
			return fmt.Errorf("%w: message at index %d contains invalid UTF-8", ErrValidation, i)
		}
		if strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf("%w: message at index %d content is empty", ErrValidation, i)
		}
		if len([]byte(item.Content)) > maxContentBytes {
			return fmt.Errorf("%w: message at index %d content size %d bytes exceeds limit %d bytes", ErrValidation, i, len([]byte(item.Content)), maxContentBytes)
		}
		if utf8.RuneCountInString(item.Name) > maxNameRunes {
			return fmt.Errorf("%w: message at index %d name length %d characters exceeds limit %d", ErrValidation, i, utf8.RuneCountInString(item.Name), maxNameRunes)
		}
		item.Name = strings.TrimSpace(item.Name)
		if strings.TrimSpace(item.ID) == "" {
			if !generateIDs {
				return fmt.Errorf("%w: message at index %d id is empty", ErrValidation, i)
			}
			item.ID = "preset-" + uuid.NewString()
		}
		if strings.ContainsAny(item.ID, "/\\") || strings.Contains(item.ID, "..") {
			return fmt.Errorf("%w: message at index %d has invalid id %q", ErrValidation, i, item.ID)
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("%w: message id %q is duplicated", ErrValidation, item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	if config.Messages == nil {
		config.Messages = []model.MessagePreset{}
	}
	return nil
}

func (s *serviceImpl) validateStored(stored *repository.StoredMessagePresetConfig, global bool, workspaceID string) error {
	if err := validateAndNormalize(stored.Config, global, false); err != nil {
		return fmt.Errorf("validate message preset configuration %s: %w", stored.File, err)
	}
	if !global && stored.Revision != "missing" && stored.Config.WorkspaceID != workspaceID {
		return fmt.Errorf("validate message preset configuration %s: workspaceId %q does not match file workspace id %q", stored.File, stored.Config.WorkspaceID, workspaceID)
	}
	return nil
}

func (s *serviceImpl) GetGlobal() (*model.MessagePresetScopeResponse, error) {
	stored, err := s.repo.GetGlobal()
	if err != nil {
		return nil, err
	}
	if err := s.validateStored(stored, true, ""); err != nil {
		return nil, err
	}
	return responseFromStored(stored), nil
}

func (s *serviceImpl) GetWorkspace(workspaceID string) (*model.MessagePresetScopeResponse, error) {
	ws, ok := s.workspaces.Get(workspaceID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, workspaceID)
	}
	stored, err := s.repo.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	if stored.Revision == "missing" {
		stored.Config.WorkspaceID = ws.ID
		stored.Config.WorkspaceTitle = ws.Title
		stored.Config.WorkspaceWorkdir = ws.Workdir
	}
	if err := s.validateStored(stored, false, workspaceID); err != nil {
		return nil, err
	}
	return responseFromStored(stored), nil
}

func (s *serviceImpl) GetEffective(workspaceID string) (*model.EffectiveMessagePresetsResponse, error) {
	if _, ok := s.workspaces.Get(workspaceID); !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, workspaceID)
	}
	result := &model.EffectiveMessagePresetsResponse{
		Code: 0, WorkspaceID: workspaceID,
		Project: []model.MessagePreset{}, Global: []model.MessagePreset{},
	}
	project, projectErr := s.repo.GetWorkspace(workspaceID)
	if projectErr == nil {
		projectErr = s.validateStored(project, false, workspaceID)
	}
	if projectErr != nil {
		file, _ := typepath.WorkspaceMessagePresetsFile(workspaceID)
		if project != nil {
			file = project.File
		}
		result.Errors = append(result.Errors, model.MessagePresetLoadError{Scope: "project", File: file, Error: projectErr.Error()})
	} else {
		result.Project = cloneMessages(project.Config.Messages)
	}
	global, globalErr := s.repo.GetGlobal()
	if globalErr == nil {
		globalErr = s.validateStored(global, true, "")
	}
	if globalErr != nil {
		file, _ := typepath.GlobalMessagePresetsFile()
		if global != nil {
			file = global.File
		}
		result.Errors = append(result.Errors, model.MessagePresetLoadError{Scope: "global", File: file, Error: globalErr.Error()})
	} else {
		result.Global = cloneMessages(global.Config.Messages)
	}
	return result, nil
}

func (s *serviceImpl) SaveGlobal(req model.SaveMessagePresetScopeRequest) (*model.MessagePresetScopeResponse, error) {
	config := &model.MessagePresetConfig{SchemaVersion: model.MessagePresetSchemaVersion, Messages: cloneMessages(req.Messages)}
	if err := validateAndNormalize(config, true, true); err != nil {
		return nil, err
	}
	stored, err := s.repo.SaveGlobal(req.Revision, config)
	if err != nil {
		return nil, err
	}
	return responseFromStored(stored), nil
}

func (s *serviceImpl) SaveWorkspace(workspaceID string, req model.SaveMessagePresetScopeRequest) (*model.MessagePresetScopeResponse, error) {
	ws, ok := s.workspaces.Get(workspaceID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, workspaceID)
	}
	config := &model.MessagePresetConfig{
		SchemaVersion: model.MessagePresetSchemaVersion, WorkspaceID: ws.ID,
		WorkspaceTitle: ws.Title, WorkspaceWorkdir: ws.Workdir, Messages: cloneMessages(req.Messages),
	}
	if err := validateAndNormalize(config, false, true); err != nil {
		return nil, err
	}
	stored, err := s.repo.SaveWorkspace(workspaceID, req.Revision, config)
	if err != nil {
		return nil, err
	}
	if stored.Revision == "missing" {
		stored.Config.WorkspaceID = ws.ID
		stored.Config.WorkspaceTitle = ws.Title
		stored.Config.WorkspaceWorkdir = ws.Workdir
	}
	return responseFromStored(stored), nil
}

func (s *serviceImpl) ListOrphans() (*model.ListOrphanMessagePresetsResponse, error) {
	files, err := s.repo.ListWorkspaceFiles()
	if err != nil {
		return nil, err
	}
	result := &model.ListOrphanMessagePresetsResponse{Code: 0, Configs: []model.OrphanMessagePreset{}}
	for _, file := range files {
		workspaceID := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		if _, ok := s.workspaces.Get(workspaceID); ok {
			continue
		}
		stored, loadErr := s.repo.GetWorkspace(workspaceID)
		if loadErr == nil {
			loadErr = s.validateStored(stored, false, workspaceID)
		}
		if loadErr != nil {
			result.Errors = append(result.Errors, model.MessagePresetLoadError{Scope: "orphan", File: file, Error: loadErr.Error()})
			continue
		}
		result.Configs = append(result.Configs, model.OrphanMessagePreset{Revision: stored.Revision, Config: *stored.Config})
	}
	return result, nil
}

func (s *serviceImpl) DeleteOrphan(workspaceID, revision string) error {
	if _, ok := s.workspaces.Get(workspaceID); ok {
		return fmt.Errorf("%w: workspace %q still exists; only unbound configurations can be deleted", ErrConflict, workspaceID)
	}
	return s.repo.DeleteWorkspace(workspaceID, revision)
}

func (s *serviceImpl) RebindOrphan(sourceWorkspaceID string, req model.RebindMessagePresetRequest) error {
	if _, ok := s.workspaces.Get(sourceWorkspaceID); ok {
		return fmt.Errorf("%w: workspace %q still exists; its configuration is not unbound", ErrConflict, sourceWorkspaceID)
	}
	target, ok := s.workspaces.Get(req.TargetWorkspaceID)
	if !ok {
		return fmt.Errorf("%w: target workspace %s", ErrNotFound, req.TargetWorkspaceID)
	}
	stored, err := s.repo.GetWorkspace(sourceWorkspaceID)
	if err != nil {
		return err
	}
	if err := s.validateStored(stored, false, sourceWorkspaceID); err != nil {
		return err
	}
	config := *stored.Config
	config.Messages = cloneMessages(stored.Config.Messages)
	config.WorkspaceID = target.ID
	config.WorkspaceTitle = target.Title
	config.WorkspaceWorkdir = target.Workdir
	return s.repo.RebindWorkspace(sourceWorkspaceID, target.ID, req.Revision, &config)
}
