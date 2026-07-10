package config

import (
	"strings"
	"sync"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/probe"
)

type SettingsService interface {
	GetSettings() (*repository.Settings, error)
	SaveSettings(s *repository.Settings) error
	GetACPEnvVars(agentType string) map[string]string
	GetLarkConfig() (appID, appSecret string)
	GetLarkIMSenderIDs() (adminSenderID, sophiaSenderID string)
	GetIMConfig() (workspaceID string, agent *repository.AgentConfig)

	// WeChat admin whitelist — populated via scan-to-login + first-contact
	// approval (see doc_wechat_integration.md §5.1). WeChat does not have a
	// bot identity concept comparable to Lark's sophia_sender_id, so there
	// is no matching "GetWeChatBotID" method — group chats are unsupported
	// in WeChat anyway (iLink bot_type=3 is P2P only).
	GetWeChatAdminIDs() []string
	AddWeChatAdminID(id string) error
	RemoveWeChatAdminID(id string) error
}

type settingsServiceImpl struct {
	repo repository.SettingsRepo
	// adminMu serialises the read-modify-write sequence in
	// AddWeChatAdminID / RemoveWeChatAdminID so two concurrent admin
	// changes don't race through the Get/Save cycle and lose an update.
	// The repo has its own RWMutex that makes each Get/Save atomic, but
	// that is not enough for compound operations.
	adminMu sync.Mutex
}

func NewSettingsService() (SettingsService, error) {
	repo, err := repository.NewSettingsRepo()
	if err != nil {
		return nil, err
	}
	return &settingsServiceImpl{repo: repo}, nil
}

func (s *settingsServiceImpl) GetSettings() (*repository.Settings, error) {
	settings, err := s.repo.Get()
	if err != nil {
		return nil, err
	}
	normalizeACPEnvVars(settings)
	return settings, nil
}

func (s *settingsServiceImpl) SaveSettings(settings *repository.Settings) error {
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	current, err := s.repo.Get()
	if err != nil {
		return err
	}
	if current != nil {
		// The generic settings form does not own WeChat admin-whitelist edits;
		// those go through Add/RemoveWeChatAdminID. Preserve the latest
		// whitelist here so a stale web snapshot cannot overwrite a concurrent
		// IM-triggered admin add/remove.
		settings.WeChatAdminIDs = append([]string(nil), current.WeChatAdminIDs...)
	}
	normalizeACPEnvVars(settings)
	return s.repo.Save(settings)
}

func (s *settingsServiceImpl) GetACPEnvVars(agentType string) map[string]string {
	settings, err := s.repo.Get()
	if err != nil || settings == nil || len(settings.ACPEnvVars) == 0 {
		return nil
	}
	normalizeACPEnvVars(settings)
	envList := envVarsForAgent(settings.ACPEnvVars, agentType)
	if len(envList) == 0 {
		return nil
	}
	result := make(map[string]string)
	for _, entry := range envList {
		if entry.Enabled && entry.Key != "" {
			result[entry.Key] = entry.Value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizeACPEnvVars(settings *repository.Settings) {
	if settings == nil || len(settings.ACPEnvVars) == 0 {
		return
	}
	type rankedEnvVars struct {
		vars     []repository.ACPEnvVarEntry
		priority int
	}
	byKey := make(map[string]rankedEnvVars, len(settings.ACPEnvVars))
	for savedKey, entries := range settings.ACPEnvVars {
		envKey, priority := probe.ACPAgentEnvKeyPriority(savedKey)
		current, ok := byKey[envKey]
		if !ok {
			byKey[envKey] = rankedEnvVars{vars: cloneACPEnvVarEntries(entries), priority: priority}
			continue
		}
		mergedPriority := current.priority
		if priority > mergedPriority {
			mergedPriority = priority
		}
		byKey[envKey] = rankedEnvVars{
			vars:     mergeACPEnvVarEntries(current.vars, entries, current.priority, priority),
			priority: mergedPriority,
		}
	}
	normalized := make(map[string][]repository.ACPEnvVarEntry, len(byKey))
	for key, ranked := range byKey {
		if len(ranked.vars) > 0 {
			normalized[key] = ranked.vars
		}
	}
	settings.ACPEnvVars = normalized
}

func envVarsForAgent(envMap map[string][]repository.ACPEnvVarEntry, agentType string) []repository.ACPEnvVarEntry {
	var out []repository.ACPEnvVarEntry
	priority := -1
	for _, key := range probe.ACPAgentEnvLookupKeys(agentType) {
		if entries := envMap[key]; len(entries) > 0 {
			_, keyPriority := probe.ACPAgentEnvKeyPriority(key)
			out = mergeACPEnvVarEntries(out, entries, priority, keyPriority)
			if keyPriority > priority {
				priority = keyPriority
			}
		}
	}
	return out
}

func mergeACPEnvVarEntries(
	base []repository.ACPEnvVarEntry,
	incoming []repository.ACPEnvVarEntry,
	basePriority int,
	incomingPriority int,
) []repository.ACPEnvVarEntry {
	type indexedEntry struct {
		entry    repository.ACPEnvVarEntry
		priority int
		index    int
	}
	out := cloneACPEnvVarEntries(base)
	byKey := make(map[string]indexedEntry, len(out))
	for i, entry := range out {
		byKey[entry.Key] = indexedEntry{entry: entry, priority: basePriority, index: i}
	}
	for _, entry := range incoming {
		if existing, ok := byKey[entry.Key]; ok {
			if shouldReplaceACPEnvVar(existing.entry, entry, existing.priority, incomingPriority) {
				out[existing.index] = entry
				byKey[entry.Key] = indexedEntry{entry: entry, priority: incomingPriority, index: existing.index}
			}
			continue
		}
		byKey[entry.Key] = indexedEntry{entry: entry, priority: incomingPriority, index: len(out)}
		out = append(out, entry)
	}
	return out
}

func shouldReplaceACPEnvVar(
	existing repository.ACPEnvVarEntry,
	incoming repository.ACPEnvVarEntry,
	existingPriority int,
	incomingPriority int,
) bool {
	if existing.Enabled != incoming.Enabled {
		return incoming.Enabled
	}
	if incomingPriority != existingPriority {
		return incomingPriority > existingPriority
	}
	if existing.Value == "" && incoming.Value != "" {
		return true
	}
	return false
}

func cloneACPEnvVarEntries(in []repository.ACPEnvVarEntry) []repository.ACPEnvVarEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]repository.ACPEnvVarEntry, len(in))
	copy(out, in)
	return out
}

func (s *settingsServiceImpl) GetLarkConfig() (appID, appSecret string) {
	settings, err := s.repo.Get()
	if err != nil {
		logger.Error("[settings] GetLarkConfig: read failed: %v", err)
		return "", ""
	}
	return settings.LarkAppID, settings.LarkAppSecret
}

func (s *settingsServiceImpl) GetLarkIMSenderIDs() (adminSenderID, sophiaSenderID string) {
	settings, err := s.repo.Get()
	if err != nil {
		logger.Error("[settings] GetLarkIMSenderIDs: read failed: %v", err)
		return "", ""
	}
	return settings.LarkIMAdminSenderID, settings.LarkIMSophiaSenderID
}

func (s *settingsServiceImpl) GetIMConfig() (workspaceID string, agent *repository.AgentConfig) {
	settings, err := s.repo.Get()
	if err != nil {
		logger.Error("[settings] GetIMConfig: read failed: %v", err)
		return "", nil
	}
	return settings.IMWorkspaceID, settings.MessageAgent
}

func (s *settingsServiceImpl) GetWeChatAdminIDs() []string {
	settings, err := s.repo.Get()
	if err != nil {
		logger.Error("[settings] GetWeChatAdminIDs: read failed: %v", err)
		return nil
	}
	if len(settings.WeChatAdminIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(settings.WeChatAdminIDs))
	for _, id := range settings.WeChatAdminIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (s *settingsServiceImpl) AddWeChatAdminID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	settings, err := s.repo.Get()
	if err != nil {
		return err
	}
	for _, existing := range settings.WeChatAdminIDs {
		if strings.TrimSpace(existing) == id {
			return nil
		}
	}
	settings.WeChatAdminIDs = append(settings.WeChatAdminIDs, id)
	return s.repo.Save(settings)
}

func (s *settingsServiceImpl) RemoveWeChatAdminID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	settings, err := s.repo.Get()
	if err != nil {
		return err
	}
	filtered := make([]string, 0, len(settings.WeChatAdminIDs))
	for _, existing := range settings.WeChatAdminIDs {
		if strings.TrimSpace(existing) == id {
			continue
		}
		filtered = append(filtered, existing)
	}
	settings.WeChatAdminIDs = filtered
	return s.repo.Save(settings)
}
