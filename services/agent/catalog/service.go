package catalog

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/types/model"
)

type Entry struct {
	Source  model.AgentCatalogSource
	Builtin *BuiltinAgent
	Custom  *model.CustomAgent
}

type ResolvedAgent struct {
	AgentID               string
	Revision              string
	RuntimeKey            string
	Definition            model.AgentRuntimeDefinition
	Command               string
	Bin                   string
	SupportsHeadlessPrint bool
	Deprecated            bool
	Lifecycle             model.AgentLifecycle
}

type Service struct {
	repo AgentCatalogRepository
	mu   sync.Mutex
}

type AgentCatalogRepository interface {
	Load(ctx context.Context) (*model.AgentCatalogSnapshot, error)
	Save(ctx context.Context, snapshot *model.AgentCatalogSnapshot) error
}

type CustomMutation func(current []model.CustomAgent) ([]model.CustomAgent, error)

func NewService() (*Service, error) {
	repo, err := repository.NewAgentCatalogRepo()
	if err != nil {
		return nil, fmt.Errorf("create Agent catalog repository failed: %w", err)
	}
	service := &Service{repo: repo}
	ctx := context.Background()
	if err := ValidateBuiltins(); err != nil {
		return nil, fmt.Errorf("validate built-in Agent catalog failed: %w", err)
	}
	if err := service.reconcileBuiltinRevisions(ctx); err != nil {
		return nil, fmt.Errorf("retain built-in Agent runtime revisions failed: %w", err)
	}
	if err := service.Validate(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

// Builtins returns the supported built-in entries in selector order.
func (s *Service) Builtins() []BuiltinAgent {
	return BuiltinSnapshot()
}

// Custom returns the persisted custom entries in creation order.
func (s *Service) Custom(ctx context.Context) ([]model.CustomAgent, error) {
	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateCustomAgents(snapshot.Agents); err != nil {
		return nil, err
	}
	return cloneCustomAgents(snapshot.Agents), nil
}

func (s *Service) BuiltinRevisions(ctx context.Context, agentID string) ([]model.AgentRuntimeRevision, error) {
	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return nil, err
	}
	revisions := snapshot.BuiltinRevisions[agentID]
	return cloneRuntimeRevisions(revisions), nil
}

// List returns built-ins first and persisted custom entries second.
func (s *Service) List(ctx context.Context) ([]Entry, error) {
	custom, err := s.Custom(ctx)
	if err != nil {
		return nil, err
	}
	builtins := s.Builtins()
	entries := make([]Entry, 0, len(builtins)+len(custom))
	for index := range builtins {
		agent := builtins[index]
		entries = append(entries, Entry{
			Source:  model.AgentCatalogSourceBuiltin,
			Builtin: &agent,
		})
	}
	for index := range custom {
		agent := custom[index]
		if agent.Lifecycle == model.AgentLifecycleDeleted {
			continue
		}
		entries = append(entries, Entry{
			Source: model.AgentCatalogSourceCustom,
			Custom: &agent,
		})
	}
	return entries, nil
}

func (s *Service) ListItems(ctx context.Context) ([]model.AgentCatalogItem, error) {
	entries, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]model.AgentCatalogItem, 0, len(entries))
	for _, entry := range entries {
		switch entry.Source {
		case model.AgentCatalogSourceBuiltin:
			if entry.Builtin == nil {
				return nil, fmt.Errorf("built-in Agent catalog entry is nil")
			}
			agent := entry.Builtin
			platform := agentinstall.CurrentPlatform()
			identifiers := []model.AgentCatalogIdentifier{{
				Kind:  string(IdentifierKindEnvKey),
				Value: agent.EnvKey,
			}}
			for _, identifier := range agent.HistoricalIdentifiers {
				identifiers = append(identifiers, model.AgentCatalogIdentifier{
					Kind:  string(identifier.Kind),
					Value: identifier.Value,
				})
			}
			items = append(items, model.AgentCatalogItem{
				AgentID:               agent.AgentID,
				Source:                model.AgentCatalogSourceBuiltin,
				DisplayName:           agent.DisplayName,
				IconURL:               agent.IconURL,
				Definition:            agent.RuntimeDefinition(),
				HistoricalIdentifiers: identifiers,
				SupportsHeadlessPrint: agent.SupportsHeadlessPrint,
				Deprecated:            agent.Deprecated,
				Lifecycle:             model.AgentLifecycleActive,
				InstallMethod:         string(agent.Install.Method),
				InstallCommands:       agent.Install.StepDisplays(platform),
				UninstallCommands:     agent.Install.UninstallStepDisplays(platform),
				InstallInstructions:   agent.Install.Instructions,
				AutoInstallable:       agent.Install.AutoInstallable(platform),
				AutoUninstallable:     agent.Install.AutoUninstallable(platform),
			})
		case model.AgentCatalogSourceCustom:
			if entry.Custom == nil {
				return nil, fmt.Errorf("custom Agent catalog entry is nil")
			}
			agent := entry.Custom
			items = append(items, model.AgentCatalogItem{
				AgentID:               agent.AgentID,
				Source:                model.AgentCatalogSourceCustom,
				DisplayName:           agent.DisplayName,
				IconURL:               agent.IconURL,
				Definition:            currentRuntimeDefinition(*agent),
				SupportsHeadlessPrint: agent.SupportsHeadlessPrint,
				Lifecycle:             agent.Lifecycle,
				CurrentRevision:       agent.CurrentRevision,
				DeleteError:           agent.DeleteError,
			})
		default:
			return nil, fmt.Errorf("unknown Agent catalog source %q", entry.Source)
		}
	}
	return items, nil
}

func (s *Service) DeletedItems(ctx context.Context) ([]model.AgentCatalogItem, error) {
	custom, err := s.Custom(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]model.AgentCatalogItem, 0)
	for index := range custom {
		agent := custom[index]
		if agent.Lifecycle != model.AgentLifecycleDeleted {
			continue
		}
		items = append(items, model.AgentCatalogItem{
			AgentID:               agent.AgentID,
			Source:                model.AgentCatalogSourceCustom,
			DisplayName:           agent.DisplayName,
			IconURL:               agent.IconURL,
			Definition:            currentRuntimeDefinition(agent),
			SupportsHeadlessPrint: agent.SupportsHeadlessPrint,
			Lifecycle:             agent.Lifecycle,
			CurrentRevision:       agent.CurrentRevision,
			Availability:          "deleted",
			DeleteError:           agent.DeleteError,
		})
	}
	return items, nil
}

func (s *Service) Find(ctx context.Context, agentID string) (Entry, bool, error) {
	if builtin, ok := FindBuiltinByID(agentID); ok {
		return Entry{
			Source:  model.AgentCatalogSourceBuiltin,
			Builtin: &builtin,
		}, true, nil
	}
	custom, err := s.Custom(ctx)
	if err != nil {
		return Entry{}, false, err
	}
	for index := range custom {
		if custom[index].AgentID == agentID {
			agent := custom[index]
			return Entry{
				Source: model.AgentCatalogSourceCustom,
				Custom: &agent,
			}, true, nil
		}
	}
	return Entry{}, false, nil
}

// Resolve accepts a stable AgentID or a built-in migration identifier and
// returns the current runtime projection. New settings persist ResolvedAgent's
// AgentID; Command and Bin are resolved only at execution time.
func (s *Service) Resolve(ctx context.Context, identifier string) (ResolvedAgent, bool, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return ResolvedAgent{}, false, nil
	}
	if builtin, ok := ResolveBuiltin(identifier); ok {
		binding := BindingForBuiltin(builtin)
		return ResolvedAgent{
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
	custom, err := s.Custom(ctx)
	if err != nil {
		return ResolvedAgent{}, false, err
	}
	for _, agent := range custom {
		if agent.AgentID != identifier {
			continue
		}
		definition := currentRuntimeDefinition(agent)
		binding, bindErr := BindingForCustom(agent, agent.CurrentRevision)
		if bindErr != nil {
			return ResolvedAgent{}, false, bindErr
		}
		return ResolvedAgent{
			AgentID:               agent.AgentID,
			Revision:              binding.Revision,
			RuntimeKey:            binding.RuntimeKey,
			Definition:            binding.Definition,
			Command:               strings.Join(append([]string{definition.ACPProgram}, definition.ACPArgs...), " "),
			Bin:                   definition.Bin,
			SupportsHeadlessPrint: agent.SupportsHeadlessPrint,
			Lifecycle:             agent.Lifecycle,
		}, true, nil
	}
	return ResolvedAgent{}, false, nil
}

func (s *Service) ResolveBinding(ctx context.Context, identifier, revision string) (model.AgentRuntimeBinding, bool, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return model.AgentRuntimeBinding{}, false, nil
	}
	if builtin, ok := ResolveBuiltin(identifier); ok {
		current := BindingForBuiltin(builtin)
		if revision == "" {
			return current, true, nil
		}
		revisions, err := s.BuiltinRevisions(ctx, builtin.AgentID)
		if err != nil {
			return model.AgentRuntimeBinding{}, false, err
		}
		for _, retained := range revisions {
			if retained.Revision != revision {
				continue
			}
			return model.AgentRuntimeBinding{
				AgentID:    builtin.AgentID,
				Revision:   retained.Revision,
				RuntimeKey: RuntimeKey(builtin.AgentID, retained.Revision),
				Definition: retained.Definition,
			}, true, nil
		}
		return model.AgentRuntimeBinding{}, false, fmt.Errorf(
			"built-in AgentID %q runtime revision %q is not retained; current=%q",
			builtin.AgentID,
			revision,
			current.Revision,
		)
	}
	custom, err := s.Custom(ctx)
	if err != nil {
		return model.AgentRuntimeBinding{}, false, err
	}
	for _, agent := range custom {
		if agent.AgentID != identifier {
			continue
		}
		binding, err := BindingForCustom(agent, revision)
		if err != nil {
			return model.AgentRuntimeBinding{}, false, err
		}
		return binding, true, nil
	}
	return model.AgentRuntimeBinding{}, false, nil
}

func (s *Service) ResolveLegacyBinding(ctx context.Context, identifier string) (model.AgentRuntimeBinding, bool, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return model.AgentRuntimeBinding{}, false, nil
	}
	if builtin, ok := ResolveBuiltin(identifier); ok {
		binding, err := LegacyBindingForBuiltin(builtin, identifier)
		if err != nil {
			return model.AgentRuntimeBinding{}, false, err
		}
		if err := s.retainBuiltinBinding(ctx, binding); err != nil {
			return model.AgentRuntimeBinding{}, false, fmt.Errorf(
				"retain built-in AgentID %q runtime revision %q failed: %w",
				binding.AgentID,
				binding.Revision,
				err,
			)
		}
		return binding, true, nil
	}
	custom, err := s.Custom(ctx)
	if err != nil {
		return model.AgentRuntimeBinding{}, false, err
	}
	for _, agent := range custom {
		if agent.AgentID != identifier {
			continue
		}
		binding, err := BindingForCustom(agent, agent.CurrentRevision)
		if err != nil {
			return model.AgentRuntimeBinding{}, false, err
		}
		return binding, true, nil
	}
	return model.AgentRuntimeBinding{}, false, nil
}

// SaveCustom atomically replaces the custom portion after validating the whole
// directory against built-in IDs and definitions. Management flows should
// perform their operation-specific validation first, then commit one complete
// snapshot through this method.
func (s *Service) SaveCustom(ctx context.Context, agents []model.CustomAgent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateCustomAgents(agents); err != nil {
		return err
	}
	current, err := s.repo.Load(ctx)
	if err != nil {
		return fmt.Errorf("load current custom Agent catalog before save failed: %w", err)
	}
	nextIDs := make(map[string]bool, len(agents))
	for _, agent := range agents {
		nextIDs[agent.AgentID] = true
	}
	for _, agent := range current.Agents {
		if !nextIDs[agent.AgentID] {
			return fmt.Errorf(
				"custom Agent %q cannot be removed from the catalog; keep the immutable AgentID and change its lifecycle",
				agent.AgentID,
			)
		}
	}
	snapshot := &model.AgentCatalogSnapshot{
		Version:          model.AgentCatalogVersion,
		Agents:           cloneCustomAgents(agents),
		BuiltinRevisions: cloneBuiltinRevisionMap(current.BuiltinRevisions),
	}
	return s.repo.Save(ctx, snapshot)
}

// MutateCustom serializes the full load-validate-save transaction. Management
// handlers use this instead of a separate Custom + SaveCustom sequence so two
// concurrent operations cannot overwrite each other's catalog changes.
func (s *Service) MutateCustom(ctx context.Context, mutate CustomMutation) ([]model.CustomAgent, error) {
	if mutate == nil {
		return nil, fmt.Errorf("custom Agent mutation is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.repo.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load custom Agent catalog before mutation failed: %w", err)
	}
	next, err := mutate(cloneCustomAgents(current.Agents))
	if err != nil {
		return nil, err
	}
	if err := validateCustomAgents(next); err != nil {
		return nil, err
	}
	currentIDs := make(map[string]bool, len(current.Agents))
	for _, agent := range current.Agents {
		currentIDs[agent.AgentID] = true
	}
	nextIDs := make(map[string]bool, len(next))
	for _, agent := range next {
		nextIDs[agent.AgentID] = true
	}
	for agentID := range currentIDs {
		if !nextIDs[agentID] {
			return nil, fmt.Errorf(
				"custom Agent %q cannot be removed from the catalog; keep the immutable AgentID and change its lifecycle",
				agentID,
			)
		}
	}
	snapshot := &model.AgentCatalogSnapshot{
		Version:          model.AgentCatalogVersion,
		Agents:           cloneCustomAgents(next),
		BuiltinRevisions: cloneBuiltinRevisionMap(current.BuiltinRevisions),
	}
	if err := s.repo.Save(ctx, snapshot); err != nil {
		return nil, err
	}
	return cloneCustomAgents(next), nil
}

func (s *Service) Validate(ctx context.Context) error {
	if err := ValidateBuiltins(); err != nil {
		return fmt.Errorf("validate built-in Agent catalog failed: %w", err)
	}
	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return fmt.Errorf("load custom Agent catalog failed: %w", err)
	}
	if err := validateCustomAgents(snapshot.Agents); err != nil {
		return fmt.Errorf("validate custom Agent catalog failed: %w", err)
	}
	if err := validateBuiltinRevisions(snapshot.BuiltinRevisions); err != nil {
		return fmt.Errorf("validate retained built-in Agent revisions failed: %w", err)
	}
	return nil
}

func validateBuiltinRevisions(revisionsByAgent map[string][]model.AgentRuntimeRevision) error {
	for agentID, revisions := range revisionsByAgent {
		if _, found := FindBuiltinByID(agentID); !found {
			return fmt.Errorf("retained built-in revisions reference unknown AgentID %q", agentID)
		}
		seen := make(map[string]bool, len(revisions))
		for _, revision := range revisions {
			if strings.TrimSpace(revision.Definition.Bin) == "" ||
				strings.TrimSpace(revision.Definition.ACPProgram) == "" {
				return fmt.Errorf(
					"retained built-in AgentID %q revision %q has an incomplete runtime definition",
					agentID,
					revision.Revision,
				)
			}
			expected := RevisionForDefinition(revision.Definition)
			if revision.Revision != expected {
				return fmt.Errorf(
					"retained built-in AgentID %q revision %q is not content-addressed; expected %q",
					agentID,
					revision.Revision,
					expected,
				)
			}
			if seen[revision.Revision] {
				return fmt.Errorf(
					"retained built-in AgentID %q has duplicate revision %q",
					agentID,
					revision.Revision,
				)
			}
			seen[revision.Revision] = true
		}
	}
	return nil
}

func (s *Service) reconcileBuiltinRevisions(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return err
	}
	if snapshot.BuiltinRevisions == nil {
		snapshot.BuiltinRevisions = make(map[string][]model.AgentRuntimeRevision)
	}
	changed := false
	builtins := BuiltinSnapshot()
	builtinIDs := make(map[string]bool, len(builtins))
	for _, builtin := range builtins {
		builtinIDs[builtin.AgentID] = true
	}
	for agentID := range snapshot.BuiltinRevisions {
		if builtinIDs[agentID] {
			continue
		}
		logger.Warnf(ctx, "[agent.catalog] prune retained revisions for removed built-in AgentID %q", agentID)
		delete(snapshot.BuiltinRevisions, agentID)
		changed = true
	}
	for _, builtin := range builtins {
		if builtin.Deprecated {
			continue
		}
		candidate := model.AgentRuntimeRevision{
			Revision:   RevisionForDefinition(builtin.RuntimeDefinition()),
			Definition: builtin.RuntimeDefinition(),
		}
		known := make(map[string]bool, len(snapshot.BuiltinRevisions[builtin.AgentID]))
		for _, retained := range snapshot.BuiltinRevisions[builtin.AgentID] {
			known[retained.Revision] = true
		}
		if !known[candidate.Revision] {
			snapshot.BuiltinRevisions[builtin.AgentID] = append(
				snapshot.BuiltinRevisions[builtin.AgentID],
				candidate,
			)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.repo.Save(ctx, snapshot)
}

func (s *Service) retainBuiltinBinding(
	ctx context.Context,
	binding model.AgentRuntimeBinding,
) error {
	if _, found := FindBuiltinByID(binding.AgentID); !found {
		return fmt.Errorf("AgentID %q is not built-in", binding.AgentID)
	}
	if binding.Revision != RevisionForDefinition(binding.Definition) {
		return fmt.Errorf(
			"runtime revision %q is not content-addressed for AgentID %q",
			binding.Revision,
			binding.AgentID,
		)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return err
	}
	for _, retained := range snapshot.BuiltinRevisions[binding.AgentID] {
		if retained.Revision == binding.Revision {
			return nil
		}
	}
	snapshot.BuiltinRevisions[binding.AgentID] = append(
		snapshot.BuiltinRevisions[binding.AgentID],
		model.AgentRuntimeRevision{
			Revision:   binding.Revision,
			Definition: binding.Definition,
		},
	)
	return s.repo.Save(ctx, snapshot)
}

func validateCustomAgents(agents []model.CustomAgent) error {
	builtins := BuiltinSnapshot()
	builtinIDs := make(map[string]bool, len(builtins))
	definitionOwners := make(map[string]string, len(builtins)+len(agents))
	for _, builtin := range builtins {
		builtinIDs[builtin.AgentID] = true
		definitionOwners[runtimeDefinitionKey(builtin.RuntimeDefinition())] = builtin.AgentID
		for _, historical := range builtin.HistoricalIdentifiers {
			if historical.Kind != IdentifierKindACPCommand {
				continue
			}
			definition, err := LegacyDefinition(historical.Value)
			if err != nil {
				return err
			}
			definition.Bin = builtin.Bin
			key := runtimeDefinitionKey(definition)
			if owner, exists := definitionOwners[key]; exists && owner != builtin.AgentID {
				return fmt.Errorf(
					"built-in Agent historical runtime definition %q conflicts with Agent %q",
					historical.Value,
					owner,
				)
			}
			definitionOwners[key] = builtin.AgentID
		}
	}

	agentIDs := make(map[string]bool, len(agents))
	for agentIndex, agent := range agents {
		if strings.TrimSpace(agent.AgentID) == "" {
			return fmt.Errorf("custom Agent at index %d has an empty AgentID", agentIndex)
		}
		if builtinIDs[agent.AgentID] {
			return fmt.Errorf("custom AgentID %q conflicts with a built-in Agent", agent.AgentID)
		}
		if agentIDs[agent.AgentID] {
			return fmt.Errorf("duplicate custom AgentID %q", agent.AgentID)
		}
		agentIDs[agent.AgentID] = true
		if strings.TrimSpace(agent.DisplayName) == "" {
			return fmt.Errorf("custom Agent %q has an empty DisplayName", agent.AgentID)
		}
		switch agent.Lifecycle {
		case model.AgentLifecycleActive, model.AgentLifecycleDeleting, model.AgentLifecycleDeleted:
		default:
			return fmt.Errorf("custom Agent %q has invalid lifecycle %q", agent.AgentID, agent.Lifecycle)
		}
		if strings.TrimSpace(agent.CurrentRevision) == "" {
			return fmt.Errorf("custom Agent %q has an empty current revision", agent.AgentID)
		}

		revisions := make(map[string]bool, len(agent.Revisions))
		hasCurrent := false
		for revisionIndex, revision := range agent.Revisions {
			if strings.TrimSpace(revision.Revision) == "" {
				return fmt.Errorf(
					"custom Agent %q revision at index %d has an empty revision ID",
					agent.AgentID,
					revisionIndex,
				)
			}
			if revisions[revision.Revision] {
				return fmt.Errorf("custom Agent %q has duplicate revision %q", agent.AgentID, revision.Revision)
			}
			revisions[revision.Revision] = true
			expectedRevision := RevisionForDefinition(revision.Definition)
			if revision.Revision != expectedRevision {
				return fmt.Errorf(
					"custom Agent %q revision %q is not content-addressed; expected %q",
					agent.AgentID,
					revision.Revision,
					expectedRevision,
				)
			}
			if revision.Revision == agent.CurrentRevision {
				hasCurrent = true
			}
			if strings.TrimSpace(revision.Definition.Bin) == "" ||
				strings.TrimSpace(revision.Definition.ACPProgram) == "" {
				return fmt.Errorf(
					"custom Agent %q revision %q has an incomplete runtime definition: Bin and ACPProgram are required",
					agent.AgentID,
					revision.Revision,
				)
			}
		}
		if !hasCurrent {
			return fmt.Errorf(
				"custom Agent %q current revision %q is missing from revisions",
				agent.AgentID,
				agent.CurrentRevision,
			)
		}

		currentDefinition := currentRuntimeDefinition(agent)
		key := runtimeDefinitionKey(currentDefinition)
		if owner, exists := definitionOwners[key]; exists && owner != agent.AgentID {
			return fmt.Errorf(
				"custom Agent %q current runtime revision %q conflicts with Agent %q",
				agent.AgentID,
				agent.CurrentRevision,
				owner,
			)
		}
		// Only current custom definitions are unique directory identities.
		// Historical definitions may coexist across AgentIDs while retained by
		// sessions or resumable runs; their runtime keys remain AgentID-scoped.
		definitionOwners[key] = agent.AgentID
	}
	return nil
}

func (s *Service) PruneUnreferencedCustomRevisions(
	ctx context.Context,
	referenced map[string]map[string]bool,
) ([]model.AgentRuntimeBinding, error) {
	var removed []model.AgentRuntimeBinding
	_, err := s.MutateCustom(ctx, func(agents []model.CustomAgent) ([]model.CustomAgent, error) {
		for agentIndex := range agents {
			agent := &agents[agentIndex]
			kept := make([]model.AgentRuntimeRevision, 0, len(agent.Revisions))
			for _, revision := range agent.Revisions {
				if revision.Revision == agent.CurrentRevision ||
					referenced[agent.AgentID][revision.Revision] {
					kept = append(kept, revision)
					continue
				}
				removed = append(removed, model.AgentRuntimeBinding{
					AgentID:    agent.AgentID,
					Revision:   revision.Revision,
					RuntimeKey: RuntimeKey(agent.AgentID, revision.Revision),
					Definition: revision.Definition,
				})
			}
			agent.Revisions = kept
		}
		return agents, nil
	})
	if err != nil {
		return nil, err
	}
	return removed, nil
}

func (s *Service) PruneUnreferencedBuiltinRevisions(
	ctx context.Context,
	referenced map[string]map[string]bool,
) ([]model.AgentRuntimeBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return nil, err
	}
	var removed []model.AgentRuntimeBinding
	changed := false
	for _, builtin := range BuiltinSnapshot() {
		current := ""
		if !builtin.Deprecated {
			current = BindingForBuiltin(builtin).Revision
		}
		revisions := snapshot.BuiltinRevisions[builtin.AgentID]
		kept := make([]model.AgentRuntimeRevision, 0, len(revisions))
		for _, revision := range revisions {
			if (current != "" && revision.Revision == current) ||
				referenced[builtin.AgentID][revision.Revision] {
				kept = append(kept, revision)
				continue
			}
			removed = append(removed, model.AgentRuntimeBinding{
				AgentID:    builtin.AgentID,
				Revision:   revision.Revision,
				RuntimeKey: RuntimeKey(builtin.AgentID, revision.Revision),
				Definition: revision.Definition,
			})
			changed = true
		}
		snapshot.BuiltinRevisions[builtin.AgentID] = kept
	}
	if !changed {
		return nil, nil
	}
	if err := s.repo.Save(ctx, snapshot); err != nil {
		return nil, err
	}
	return removed, nil
}

func currentRuntimeDefinition(agent model.CustomAgent) model.AgentRuntimeDefinition {
	for _, revision := range agent.Revisions {
		if revision.Revision == agent.CurrentRevision {
			return cloneRuntimeDefinition(revision.Definition)
		}
	}
	return cloneRuntimeDefinition(model.AgentRuntimeDefinition{})
}

func cloneRuntimeDefinition(definition model.AgentRuntimeDefinition) model.AgentRuntimeDefinition {
	definition.ACPArgs = append([]string{}, definition.ACPArgs...)
	return definition
}

func runtimeDefinitionKey(definition model.AgentRuntimeDefinition) string {
	if len(definition.ACPArgs) == 0 {
		definition.ACPArgs = nil
	}
	var builder strings.Builder
	appendKeyPart := func(value string) {
		fmt.Fprintf(&builder, "%d:%s", len(value), value)
	}
	appendKeyPart(definition.Bin)
	appendKeyPart(definition.ACPProgram)
	for _, arg := range definition.ACPArgs {
		appendKeyPart(arg)
	}
	return builder.String()
}

func DefinitionEqual(a, b model.AgentRuntimeDefinition) bool {
	return runtimeDefinitionKey(a) == runtimeDefinitionKey(b)
}

func DefinitionReservationKey(definition model.AgentRuntimeDefinition) string {
	return runtimeDefinitionKey(definition)
}

func cloneCustomAgents(agents []model.CustomAgent) []model.CustomAgent {
	if agents == nil {
		return []model.CustomAgent{}
	}
	result := make([]model.CustomAgent, len(agents))
	for agentIndex, agent := range agents {
		result[agentIndex] = agent
		result[agentIndex].Revisions = make([]model.AgentRuntimeRevision, len(agent.Revisions))
		for revisionIndex, revision := range agent.Revisions {
			result[agentIndex].Revisions[revisionIndex] = revision
			result[agentIndex].Revisions[revisionIndex].Definition =
				cloneRuntimeDefinition(revision.Definition)
		}
	}
	return result
}

func cloneRuntimeRevisions(revisions []model.AgentRuntimeRevision) []model.AgentRuntimeRevision {
	if revisions == nil {
		return nil
	}
	result := make([]model.AgentRuntimeRevision, len(revisions))
	for index, revision := range revisions {
		result[index] = revision
		result[index].Definition = cloneRuntimeDefinition(revision.Definition)
	}
	return result
}

func cloneBuiltinRevisionMap(
	in map[string][]model.AgentRuntimeRevision,
) map[string][]model.AgentRuntimeRevision {
	if in == nil {
		return nil
	}
	out := make(map[string][]model.AgentRuntimeRevision, len(in))
	for agentID, revisions := range in {
		out[agentID] = cloneRuntimeRevisions(revisions)
	}
	return out
}
