package config

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/catalog"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/model"
)

type SettingsService interface {
	GetSettings() (*model.Settings, error)
	SaveSettings(s *model.Settings) error
	SaveTitleGenerationAgent(config *model.AgentRoleConfig) error
	SaveGroupReplyAgent(config *model.AgentRoleConfig) error
	SaveIMSessionAgent(agent *model.IMSessionAgentConfig) error
	SaveACPEnvVars(agentID string, entries []model.ACPEnvVarEntry) (version int64, changed bool, err error)
	StageACPEnvVars(agentID string, entries []model.ACPEnvVarEntry) (int64, error)
	RestoreACPEnvState(agentID string, expectedVersion int64, entries []model.ACPEnvVarEntry, version int64) error
	SaveAgentPrefs(agentID string, prefs model.AgentPrefs) error
	ClearAgentSettings(agentID string) error
	GetACPEnvVars(agentType string) map[string]string
	GetACPEnvVersion(agentType string) int64
	GetLarkConfig() (appID, appSecret string)
	GetLarkIMSenderIDs() (adminSenderID, sophiaSenderID string)
	GetIMConfig() (workspaceID string, agent *model.IMSessionAgentConfig)
	GetGroupReplyAgent() *model.AgentRoleConfig

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
	repo         repository.SettingsRepo
	agentCatalog *catalog.Service
	// settingsMu serialises every read-modify-write sequence so independent
	// Agent-role endpoints and stale general-settings pages cannot overwrite
	// one another.
	// The repo has its own RWMutex that makes each Get/Save atomic, but
	// that is not enough for compound operations.
	settingsMu sync.Mutex
}

func NewSettingsService() (SettingsService, error) {
	repo, err := repository.NewSettingsRepo()
	if err != nil {
		return nil, err
	}
	agentCatalog, err := catalog.NewService()
	if err != nil {
		return nil, err
	}
	return &settingsServiceImpl{repo: repo, agentCatalog: agentCatalog}, nil
}

func (s *settingsServiceImpl) GetSettings() (*model.Settings, error) {
	settings, err := s.repo.Get()
	if err != nil {
		return nil, err
	}
	normalizeACPEnvVars(settings)
	normalizeAgentPrefs(settings)
	return settings, nil
}

func (s *settingsServiceImpl) SaveSettings(settings *model.Settings) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
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
		// Agent role settings have dedicated owners. Preserve the latest
		// service-side values so an old or concurrently opened general page
		// cannot overwrite them with a stale snapshot.
		settings.TitleGenerationAgent = cloneAgentRoleConfig(current.TitleGenerationAgent)
		settings.GroupReplyAgent = cloneAgentRoleConfig(current.GroupReplyAgent)
		settings.IMSessionAgent = cloneIMSessionAgentConfig(current.IMSessionAgent)
		settings.ACPEnvVars = cloneACPEnvVarMap(current.ACPEnvVars)
		settings.ACPEnvVersions = cloneInt64Map(current.ACPEnvVersions)
		settings.AgentPrefs = cloneAgentPrefsMap(current.AgentPrefs)
	}
	normalizeACPEnvVars(settings)
	normalizeAgentPrefs(settings)
	return s.repo.Save(settings)
}

func (s *settingsServiceImpl) SaveACPEnvVars(
	agentID string,
	entries []model.ACPEnvVarEntry,
) (int64, bool, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return 0, false, fmt.Errorf("AgentID is required")
	}
	entry, found, err := s.findAgent(context.Background(), agentID)
	if err != nil {
		return 0, false, fmt.Errorf("validate ACP environment AgentID %q failed: %w", agentID, err)
	}
	if !found {
		return 0, false, fmt.Errorf("validate ACP environment AgentID %q failed: Agent does not exist", agentID)
	}
	if err := validateAgentRoleEntry(entry, false); err != nil {
		return 0, false, fmt.Errorf("validate ACP environment AgentID %q failed: %w", agentID, err)
	}
	next := normalizeEnvEntries(entries)

	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	settings, err := s.repo.Get()
	if err != nil {
		return 0, false, err
	}
	normalizeACPEnvVars(settings)
	if settings.ACPEnvVars == nil {
		settings.ACPEnvVars = make(map[string][]model.ACPEnvVarEntry)
	}
	if settings.ACPEnvVersions == nil {
		settings.ACPEnvVersions = make(map[string]int64)
	}
	current := normalizeEnvEntries(settings.ACPEnvVars[agentID])
	configChanged := !envEntrySlicesEqual(current, next)
	effectiveChanged := !effectiveEnvEqual(current, next)
	if !configChanged {
		return settings.ACPEnvVersions[agentID], false, nil
	}
	if len(next) == 0 {
		delete(settings.ACPEnvVars, agentID)
	} else {
		settings.ACPEnvVars[agentID] = next
	}
	if effectiveChanged {
		settings.ACPEnvVersions[agentID]++
	}
	if err := s.repo.Save(settings); err != nil {
		return 0, false, err
	}
	return settings.ACPEnvVersions[agentID], effectiveChanged, nil
}

// StageACPEnvVars persists environment settings for an identity that is being
// created or restored but is not active in the catalog yet. The management
// transaction either publishes the catalog record immediately afterwards or
// calls ClearAgentSettings on failure.
func (s *settingsServiceImpl) StageACPEnvVars(
	agentID string,
	entries []model.ACPEnvVarEntry,
) (int64, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return 0, fmt.Errorf("AgentID is required")
	}
	next := normalizeEnvEntries(entries)
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	settings, err := s.repo.Get()
	if err != nil {
		return 0, err
	}
	if settings.ACPEnvVars == nil {
		settings.ACPEnvVars = make(map[string][]model.ACPEnvVarEntry)
	}
	if settings.ACPEnvVersions == nil {
		settings.ACPEnvVersions = make(map[string]int64)
	}
	if len(next) == 0 {
		delete(settings.ACPEnvVars, agentID)
	} else {
		settings.ACPEnvVars[agentID] = next
	}
	settings.ACPEnvVersions[agentID]++
	if err := s.repo.Save(settings); err != nil {
		return 0, err
	}
	return settings.ACPEnvVersions[agentID], nil
}

func (s *settingsServiceImpl) RestoreACPEnvState(
	agentID string,
	expectedVersion int64,
	entries []model.ACPEnvVarEntry,
	version int64,
) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	settings, err := s.repo.Get()
	if err != nil {
		return err
	}
	if settings.ACPEnvVersions[agentID] != expectedVersion {
		return fmt.Errorf(
			"restore Agent environment rejected because the configuration changed concurrently: AgentID=%q expectedVersion=%d currentVersion=%d",
			agentID,
			expectedVersion,
			settings.ACPEnvVersions[agentID],
		)
	}
	if len(entries) == 0 {
		delete(settings.ACPEnvVars, agentID)
	} else {
		if settings.ACPEnvVars == nil {
			settings.ACPEnvVars = make(map[string][]model.ACPEnvVarEntry)
		}
		settings.ACPEnvVars[agentID] = cloneACPEnvVarEntries(entries)
	}
	if version == 0 {
		delete(settings.ACPEnvVersions, agentID)
	} else {
		settings.ACPEnvVersions[agentID] = version
	}
	return s.repo.Save(settings)
}

func (s *settingsServiceImpl) SaveAgentPrefs(agentID string, prefs model.AgentPrefs) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return fmt.Errorf("AgentID is required")
	}
	entry, found, err := s.findAgent(context.Background(), agentID)
	if err != nil {
		return fmt.Errorf("validate Agent defaults AgentID %q failed: %w", agentID, err)
	}
	if !found {
		return fmt.Errorf("validate Agent defaults AgentID %q failed: Agent does not exist", agentID)
	}
	if err := validateAgentRoleEntry(entry, false); err != nil {
		return fmt.Errorf("validate Agent defaults AgentID %q failed: %w", agentID, err)
	}
	if err := s.validateAgentAvailable(entry); err != nil {
		return fmt.Errorf("validate Agent defaults AgentID %q failed: %w", agentID, err)
	}

	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	settings, err := s.repo.Get()
	if err != nil {
		return err
	}
	if settings.AgentPrefs == nil {
		settings.AgentPrefs = make(map[string]model.AgentPrefs)
	}
	resolved, _, _ := s.findAgent(context.Background(), agentID)
	if resolved.Command != "" && resolved.Command != agentID {
		delete(settings.AgentPrefs, resolved.Command)
	}
	if resolved.Bin != "" && resolved.Bin != agentID {
		delete(settings.AgentPrefs, resolved.Bin)
	}
	prefs.FavoriteModelIDs = uniqueStrings(prefs.FavoriteModelIDs)
	if agentPrefsEmpty(prefs) {
		delete(settings.AgentPrefs, agentID)
	} else {
		settings.AgentPrefs[agentID] = prefs
	}
	return s.repo.Save(settings)
}

func (s *settingsServiceImpl) ClearAgentSettings(agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return fmt.Errorf("AgentID is required")
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	settings, err := s.repo.Get()
	if err != nil {
		return err
	}
	keys := probe.ACPAgentEnvLookupKeys(agentID)
	if len(keys) == 0 {
		keys = []string{agentID}
	}
	for _, key := range keys {
		delete(settings.ACPEnvVars, key)
		delete(settings.ACPEnvVersions, key)
		delete(settings.AgentPrefs, key)
	}
	if settings.TitleGenerationAgent != nil && settings.TitleGenerationAgent.AgentID == agentID {
		settings.TitleGenerationAgent = nil
	}
	if settings.GroupReplyAgent != nil && settings.GroupReplyAgent.AgentID == agentID {
		settings.GroupReplyAgent = nil
	}
	if settings.IMSessionAgent != nil && settings.IMSessionAgent.AgentID == agentID {
		settings.IMSessionAgent = nil
	}
	return s.repo.Save(settings)
}

func (s *settingsServiceImpl) SaveTitleGenerationAgent(config *model.AgentRoleConfig) error {
	return s.saveOneShotAgent(config, "title generation", func(settings *model.Settings, config *model.AgentRoleConfig) {
		settings.TitleGenerationAgent = config
	})
}

func (s *settingsServiceImpl) SaveGroupReplyAgent(config *model.AgentRoleConfig) error {
	return s.saveOneShotAgent(config, "group reply", func(settings *model.Settings, config *model.AgentRoleConfig) {
		settings.GroupReplyAgent = config
	})
}

func (s *settingsServiceImpl) saveOneShotAgent(
	config *model.AgentRoleConfig,
	role string,
	apply func(*model.Settings, *model.AgentRoleConfig),
) error {
	next := cloneAgentRoleConfig(config)
	if next != nil {
		next.AgentID = strings.TrimSpace(next.AgentID)
	}
	agentID := ""
	if next != nil {
		agentID = next.AgentID
	}
	if agentID == "" {
		next = nil
	} else {
		entry, found, err := s.findAgent(context.Background(), agentID)
		if err != nil {
			return fmt.Errorf("validate %s Agent %q failed: %w", role, agentID, err)
		}
		if !found {
			return fmt.Errorf("validate %s Agent failed: AgentID %q does not exist", role, agentID)
		}
		if err := validateAgentRoleEntry(entry, true); err != nil {
			return fmt.Errorf("validate %s Agent %q failed: %w", role, agentID, err)
		}
		if err := s.validateAgentAvailable(entry); err != nil {
			return fmt.Errorf("validate %s Agent %q failed: %w", role, agentID, err)
		}
	}

	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	settings, err := s.repo.Get()
	if err != nil {
		return err
	}
	apply(settings, next)
	return s.repo.Save(settings)
}

func (s *settingsServiceImpl) SaveIMSessionAgent(agent *model.IMSessionAgentConfig) error {
	next := cloneIMSessionAgentConfig(agent)
	if next != nil {
		next.AgentID = strings.TrimSpace(next.AgentID)
		if next.AgentID == "" {
			next = nil
		} else {
			entry, found, err := s.findAgent(context.Background(), next.AgentID)
			if err != nil {
				return fmt.Errorf("validate IM session Agent %q failed: %w", next.AgentID, err)
			}
			if !found {
				return fmt.Errorf("validate IM session Agent failed: AgentID %q does not exist", next.AgentID)
			}
			if err := validateAgentRoleEntry(entry, false); err != nil {
				return fmt.Errorf("validate IM session Agent %q failed: %w", next.AgentID, err)
			}
			if err := s.validateAgentAvailable(entry); err != nil {
				return fmt.Errorf("validate IM session Agent %q failed: %w", next.AgentID, err)
			}
		}
	}

	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	settings, err := s.repo.Get()
	if err != nil {
		return err
	}
	settings.IMSessionAgent = next
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

func (s *settingsServiceImpl) GetACPEnvVersion(agentType string) int64 {
	settings, err := s.repo.Get()
	if err != nil || settings == nil {
		return 0
	}
	agentID := probe.ACPAgentEnvKey(agentType)
	return settings.ACPEnvVersions[agentID]
}

func normalizeACPEnvVars(settings *model.Settings) {
	if settings == nil || len(settings.ACPEnvVars) == 0 {
		return
	}
	type envSource struct {
		savedKey string
		envKey   string
		entries  []model.ACPEnvVarEntry
		priority int
	}
	sources := make([]envSource, 0, len(settings.ACPEnvVars))
	for savedKey, entries := range settings.ACPEnvVars {
		envKey, priority := probe.ACPAgentEnvKeyPriority(savedKey)
		sources = append(sources, envSource{
			savedKey: savedKey,
			envKey:   envKey,
			entries:  entries,
			priority: priority,
		})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].envKey != sources[j].envKey {
			return sources[i].envKey < sources[j].envKey
		}
		if sources[i].priority != sources[j].priority {
			return sources[i].priority > sources[j].priority
		}
		return sources[i].savedKey < sources[j].savedKey
	})
	normalized := make(map[string][]model.ACPEnvVarEntry)
	seen := make(map[string]map[string]bool)
	for _, source := range sources {
		if seen[source.envKey] == nil {
			seen[source.envKey] = make(map[string]bool)
		}
		for _, entry := range source.entries {
			if entry.Key == "" || seen[source.envKey][entry.Key] {
				continue
			}
			seen[source.envKey][entry.Key] = true
			normalized[source.envKey] = append(normalized[source.envKey], entry)
		}
	}
	settings.ACPEnvVars = normalized
}

func normalizeAgentPrefs(settings *model.Settings) {
	if settings == nil || len(settings.AgentPrefs) == 0 {
		return
	}
	type source struct {
		key      string
		agentID  string
		priority int
		prefs    model.AgentPrefs
	}
	sources := make([]source, 0, len(settings.AgentPrefs))
	for key, prefs := range settings.AgentPrefs {
		agentID, priority := probe.ACPAgentEnvKeyPriority(key)
		sources = append(sources, source{key: key, agentID: agentID, priority: priority, prefs: prefs})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].agentID != sources[j].agentID {
			return sources[i].agentID < sources[j].agentID
		}
		if sources[i].priority != sources[j].priority {
			return sources[i].priority > sources[j].priority
		}
		return sources[i].key < sources[j].key
	})
	normalized := make(map[string]model.AgentPrefs)
	for _, source := range sources {
		current := normalized[source.agentID]
		current.FavoriteModelIDs = uniqueStrings(append(current.FavoriteModelIDs, source.prefs.FavoriteModelIDs...))
		if current.DefaultModelID == "" {
			current.DefaultModelID = source.prefs.DefaultModelID
		}
		if current.DefaultMode == "" {
			current.DefaultMode = source.prefs.DefaultMode
		}
		if current.DefaultThoughtLevel == "" {
			current.DefaultThoughtLevel = source.prefs.DefaultThoughtLevel
		}
		normalized[source.agentID] = current
	}
	settings.AgentPrefs = normalized
}

func envVarsForAgent(envMap map[string][]model.ACPEnvVarEntry, agentType string) []model.ACPEnvVarEntry {
	type source struct {
		key      string
		entries  []model.ACPEnvVarEntry
		priority int
	}
	var sources []source
	for _, key := range probe.ACPAgentEnvLookupKeys(agentType) {
		if entries := envMap[key]; len(entries) > 0 {
			_, keyPriority := probe.ACPAgentEnvKeyPriority(key)
			sources = append(sources, source{key: key, entries: entries, priority: keyPriority})
		}
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].priority != sources[j].priority {
			return sources[i].priority > sources[j].priority
		}
		return sources[i].key < sources[j].key
	})
	var out []model.ACPEnvVarEntry
	seen := make(map[string]bool)
	for _, source := range sources {
		for _, entry := range source.entries {
			if entry.Key == "" || seen[entry.Key] {
				continue
			}
			seen[entry.Key] = true
			out = append(out, entry)
		}
	}
	return out
}

func cloneACPEnvVarEntries(in []model.ACPEnvVarEntry) []model.ACPEnvVarEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.ACPEnvVarEntry, len(in))
	copy(out, in)
	return out
}

func cloneACPEnvVarMap(in map[string][]model.ACPEnvVarEntry) map[string][]model.ACPEnvVarEntry {
	if in == nil {
		return nil
	}
	out := make(map[string][]model.ACPEnvVarEntry, len(in))
	for key, entries := range in {
		out[key] = cloneACPEnvVarEntries(entries)
	}
	return out
}

func cloneInt64Map(in map[string]int64) map[string]int64 {
	if in == nil {
		return nil
	}
	out := make(map[string]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneAgentPrefsMap(in map[string]model.AgentPrefs) map[string]model.AgentPrefs {
	if in == nil {
		return nil
	}
	out := make(map[string]model.AgentPrefs, len(in))
	for key, prefs := range in {
		prefs.FavoriteModelIDs = append([]string(nil), prefs.FavoriteModelIDs...)
		out[key] = prefs
	}
	return out
}

func normalizeEnvEntries(entries []model.ACPEnvVarEntry) []model.ACPEnvVarEntry {
	out := make([]model.ACPEnvVarEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		entry.Key = strings.TrimSpace(entry.Key)
		if entry.Key == "" || seen[entry.Key] {
			continue
		}
		seen[entry.Key] = true
		out = append(out, entry)
	}
	return out
}

func envEntrySlicesEqual(a, b []model.ACPEnvVarEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func effectiveEnvEqual(a, b []model.ACPEnvVarEntry) bool {
	return stringMapEqual(effectiveEnvMap(a), effectiveEnvMap(b))
}

func effectiveEnvMap(entries []model.ACPEnvVarEntry) map[string]string {
	result := make(map[string]string)
	for _, entry := range entries {
		if entry.Enabled && entry.Key != "" {
			result[entry.Key] = entry.Value
		}
	}
	return result
}

func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func agentPrefsEmpty(prefs model.AgentPrefs) bool {
	return len(prefs.FavoriteModelIDs) == 0 &&
		prefs.DefaultModelID == "" &&
		prefs.DefaultMode == "" &&
		prefs.DefaultThoughtLevel == ""
}

func (s *settingsServiceImpl) findAgent(ctx context.Context, identifier string) (catalog.ResolvedAgent, bool, error) {
	if s.agentCatalog == nil {
		if builtin, ok := catalog.ResolveBuiltin(identifier); ok {
			binding := catalog.BindingForBuiltin(builtin)
			return catalog.ResolvedAgent{
				AgentID:               builtin.AgentID,
				Revision:              binding.Revision,
				RuntimeKey:            binding.RuntimeKey,
				Definition:            binding.Definition,
				Command:               builtin.Command,
				Bin:                   builtin.Bin,
				SupportsHeadlessPrint: builtin.SupportsHeadlessPrint,
				Deprecated:            builtin.Deprecated,
				Lifecycle:             model.AgentLifecycleActive,
			}, true, nil
		}
		return catalog.ResolvedAgent{}, false, nil
	}
	return s.agentCatalog.Resolve(ctx, identifier)
}

func validateAgentRoleEntry(entry catalog.ResolvedAgent, requireHeadless bool) error {
	if entry.Deprecated {
		return fmt.Errorf("Agent is deprecated")
	}
	if entry.Lifecycle != model.AgentLifecycleActive {
		return fmt.Errorf("Agent lifecycle is %q, want %q", entry.Lifecycle, model.AgentLifecycleActive)
	}
	if requireHeadless && !entry.SupportsHeadlessPrint {
		return fmt.Errorf("Agent does not declare bin -p prompt support")
	}
	return nil
}

func (s *settingsServiceImpl) validateAgentAvailable(entry catalog.ResolvedAgent) error {
	status := (agentinstall.Checker{}).Check(agentinstall.Definition{
		Bin:        entry.Definition.Bin,
		ACPProgram: entry.Definition.ACPProgram,
	})
	if !status.Installed {
		return fmt.Errorf("Agent is not installed: %s", status.Error)
	}
	settings, err := s.repo.Get()
	if err != nil {
		return fmt.Errorf("read Agent environment version failed: %w", err)
	}
	envVersion := settings.ACPEnvVersions[entry.AgentID]
	validation, matched := probe.CachedAgentValidation(entry.AgentID, entry.Revision, envVersion)
	if !matched {
		return fmt.Errorf(
			"Agent availability is pending validation: AgentID=%q revision=%q envVersion=%d",
			entry.AgentID,
			entry.Revision,
			envVersion,
		)
	}
	if !validation.Success {
		return fmt.Errorf(
			"Agent is unavailable: AgentID=%q revision=%q envVersion=%d error=%s",
			entry.AgentID,
			entry.Revision,
			envVersion,
			validation.Error,
		)
	}
	return nil
}

func cloneAgentRoleConfig(in *model.AgentRoleConfig) *model.AgentRoleConfig {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneIMSessionAgentConfig(in *model.IMSessionAgentConfig) *model.IMSessionAgentConfig {
	if in == nil {
		return nil
	}
	out := *in
	return &out
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

func (s *settingsServiceImpl) GetIMConfig() (workspaceID string, agent *model.IMSessionAgentConfig) {
	settings, err := s.GetSettings()
	if err != nil {
		logger.Error("[settings] GetIMConfig: read failed: %v", err)
		return "", nil
	}
	return settings.IMWorkspaceID, cloneIMSessionAgentConfig(settings.IMSessionAgent)
}

func (s *settingsServiceImpl) GetGroupReplyAgent() *model.AgentRoleConfig {
	settings, err := s.GetSettings()
	if err != nil {
		logger.Error("[settings] GetGroupReplyAgent: read failed: %v", err)
		return nil
	}
	return cloneAgentRoleConfig(settings.GroupReplyAgent)
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
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
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
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
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
