package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/services/agent/catalog"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/model"
)

type customAgentManagementReservation struct {
	Operation string
	AgentID   string
}

type customAgentManagementCoordinator struct {
	mu          sync.Mutex
	definitions map[string]customAgentManagementReservation
	agents      map[string]string
}

var customAgentManagement = customAgentManagementCoordinator{
	definitions: make(map[string]customAgentManagementReservation),
	agents:      make(map[string]string),
}

func (m *customAgentManagementCoordinator) reserve(
	operation string,
	agentID string,
	definition *model.AgentRuntimeDefinition,
) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if agentID != "" {
		if owner, exists := m.agents[agentID]; exists {
			return nil, fmt.Errorf(
				"AgentID %q is already being managed by operation %q",
				agentID,
				owner,
			)
		}
	}
	definitionKey := ""
	if definition != nil {
		definitionKey = catalog.DefinitionReservationKey(*definition)
		if owner, exists := m.definitions[definitionKey]; exists {
			return nil, fmt.Errorf(
				"runtime definition is already reserved by operation %q for AgentID %q",
				owner.Operation,
				owner.AgentID,
			)
		}
	}
	if agentID != "" {
		m.agents[agentID] = operation
	}
	if definitionKey != "" {
		m.definitions[definitionKey] = customAgentManagementReservation{
			Operation: operation,
			AgentID:   agentID,
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if agentID != "" {
				delete(m.agents, agentID)
			}
			if definitionKey != "" {
				delete(m.definitions, definitionKey)
			}
			m.mu.Unlock()
		})
	}, nil
}

func (h *Handler) CreateCustomAgent(ctx context.Context, c *app.RequestContext) {
	var req model.CustomAgentUpsertRequest
	if !bindCustomAgentRequest(c, &req) {
		return
	}
	if err := validateCustomAgentRequest(req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	releaseReservation, err := customAgentManagement.reserve("create", "", &req.Definition)
	if err != nil {
		httputil.Conflict(c, err.Error())
		return
	}
	defer releaseReservation()
	currentAgents, err := h.agentCatalog.Custom(ctx)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.load-before-create", err)
		return
	}
	if conflict := findDefinitionConflict(currentAgents, req.Definition, ""); conflict.AgentID != "" {
		if conflict.Lifecycle == model.AgentLifecycleDeleted {
			httputil.Conflict(c, fmt.Sprintf(
				"runtime definition matches deleted AgentID %q (%s); restore that record instead of creating a new identity",
				conflict.AgentID,
				conflict.DisplayName,
			))
		} else {
			httputil.Conflict(c, fmt.Sprintf(
				"runtime definition is already owned by AgentID %q (%s)",
				conflict.AgentID,
				conflict.DisplayName,
			))
		}
		return
	}
	revision := catalog.RevisionForDefinition(req.Definition)
	candidateID, err := newCustomAgentID()
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.candidate-id", err)
		return
	}
	candidateBinding := model.AgentRuntimeBinding{
		AgentID:    "candidate-" + candidateID,
		Revision:   revision,
		RuntimeKey: catalog.RuntimeKey("candidate-"+candidateID, revision),
		Definition: req.Definition,
	}
	if err := validateCustomRuntime(candidateBinding); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}

	envVersion := int64(1)
	validation, err := probe.ValidateBindingCandidate(ctx, candidateBinding, envVersion, toACPEnv(req.Environment))
	if err != nil {
		httputil.BadRequest(c, fmt.Sprintf(
			"validate custom Agent candidate failed: revision=%q: %v",
			revision,
			err,
		))
		return
	}
	agentID, err := newCustomAgentID()
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.create-id", err)
		return
	}
	binding := model.AgentRuntimeBinding{
		AgentID:    agentID,
		Revision:   revision,
		RuntimeKey: catalog.RuntimeKey(agentID, revision),
		Definition: req.Definition,
	}
	validation.AgentID = agentID
	validation.RuntimeKey = binding.RuntimeKey
	envVersion, err = h.settingsService.StageACPEnvVars(agentID, toSettingsEnv(req.Environment))
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.stage-env", err)
		return
	}
	validation.EnvVersion = envVersion

	created := model.CustomAgent{
		AgentID:               agentID,
		DisplayName:           strings.TrimSpace(req.DisplayName),
		IconURL:               strings.TrimSpace(req.IconURL),
		SupportsHeadlessPrint: req.SupportsHeadlessPrint,
		Lifecycle:             model.AgentLifecycleActive,
		CurrentRevision:       revision,
		Revisions: []model.AgentRuntimeRevision{{
			Revision:   revision,
			Definition: req.Definition,
		}},
	}
	if _, err := h.agentCatalog.MutateCustom(ctx, func(current []model.CustomAgent) ([]model.CustomAgent, error) {
		if conflict := findDefinitionOwner(current, req.Definition, ""); conflict != "" {
			return nil, fmt.Errorf("runtime definition is already owned by AgentID %q", conflict)
		}
		return append(current, created), nil
	}); err != nil {
		if rollbackErr := h.settingsService.RestoreACPEnvState(agentID, envVersion, nil, 0); rollbackErr != nil {
			err = fmt.Errorf("%w; environment rollback failed: %v", err, rollbackErr)
		}
		h.acpProbeCache.InvalidateAgent(agentID)
		httputil.Conflict(c, err.Error())
		return
	}
	if err := registerAgentRuntimeBinding(binding); err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.register-runtime", err)
		return
	}
	probe.StoreValidation(validation)
	cacheWarning := ""
	if err := h.acpProbeCache.PersistNow(ctx); err != nil {
		cacheWarning = fmt.Sprintf(
			"custom Agent was created, but persisting its ACP validation cache failed: %v",
			err,
		)
	}
	item, err := h.catalogItemByID(ctx, agentID)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.create-response", err)
		return
	}
	c.JSON(http.StatusOK, model.CustomAgentResponse{
		Code:    0,
		Agent:   item,
		Warning: cacheWarning,
	})
}

func (h *Handler) UpdateCustomAgent(ctx context.Context, c *app.RequestContext) {
	h.upsertExistingCustomAgent(ctx, c, false)
}

func (h *Handler) RestoreCustomAgent(ctx context.Context, c *app.RequestContext) {
	h.upsertExistingCustomAgent(ctx, c, true)
}

func (h *Handler) upsertExistingCustomAgent(ctx context.Context, c *app.RequestContext, restore bool) {
	agentID := strings.TrimSpace(c.Param("agentId"))
	var req model.CustomAgentUpsertRequest
	if agentID == "" || !bindCustomAgentRequest(c, &req) {
		if agentID == "" {
			httputil.BadRequest(c, "agentId is required")
		}
		return
	}
	if err := validateCustomAgentRequest(req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	operation := "update"
	if restore {
		operation = "restore"
	}
	releaseReservation, err := customAgentManagement.reserve(operation, agentID, &req.Definition)
	if err != nil {
		httputil.Conflict(c, err.Error())
		return
	}
	defer releaseReservation()
	current, err := h.customAgentByID(ctx, agentID)
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	if restore && current.Lifecycle != model.AgentLifecycleDeleted {
		httputil.Conflict(c, fmt.Sprintf("AgentID %q lifecycle is %q, want deleted", agentID, current.Lifecycle))
		return
	}
	if !restore && current.Lifecycle != model.AgentLifecycleActive {
		httputil.Conflict(c, fmt.Sprintf("AgentID %q lifecycle is %q, want active", agentID, current.Lifecycle))
		return
	}
	allAgents, err := h.agentCatalog.Custom(ctx)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.load-before-update", err)
		return
	}
	if conflict := findDefinitionOwner(allAgents, req.Definition, agentID); conflict != "" {
		httputil.Conflict(c, fmt.Sprintf("runtime definition is already owned by AgentID %q", conflict))
		return
	}
	revision := catalog.RevisionForDefinition(req.Definition)
	binding := model.AgentRuntimeBinding{
		AgentID:    agentID,
		Revision:   revision,
		RuntimeKey: catalog.RuntimeKey(agentID, revision),
		Definition: req.Definition,
	}
	if err := validateCustomRuntime(binding); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	envVersion := h.settingsService.GetACPEnvVersion(agentID)
	validationEnv := toACPEnv(req.Environment)
	if !restore {
		if len(req.Environment) > 0 {
			httputil.BadRequest(c, "environment can only be supplied when restoring a deleted Agent; edit environment variables through the dedicated ACP settings page")
			return
		}
		settings, settingsErr := h.settingsService.GetSettings()
		if settingsErr != nil {
			httputil.InternalErrorLog(ctx, c, "agent.custom.read-env-before-edit", settingsErr)
			return
		}
		validationEnv = settingsEnvToACP(settings.ACPEnvVars[agentID])
	}
	proposedEnvVersion := envVersion
	if restore {
		proposedEnvVersion++
	}
	validation, err := probe.ValidateBindingCandidate(ctx, binding, proposedEnvVersion, validationEnv)
	if err != nil {
		httputil.BadRequest(c, fmt.Sprintf(
			"validate custom Agent failed: AgentID=%q revision=%q: %v",
			agentID,
			revision,
			err,
		))
		return
	}
	previousEnvVersion := int64(0)
	var previousEnv []model.ACPEnvVarEntry
	stagedEnvVersion := int64(0)
	if restore {
		settingsBefore, settingsErr := h.settingsService.GetSettings()
		if settingsErr != nil {
			httputil.InternalErrorLog(ctx, c, "agent.custom.read-env-before-restore", settingsErr)
			return
		}
		previousEnv = append([]model.ACPEnvVarEntry(nil), settingsBefore.ACPEnvVars[agentID]...)
		previousEnvVersion = settingsBefore.ACPEnvVersions[agentID]
		envVersion, err = h.settingsService.StageACPEnvVars(agentID, toSettingsEnv(req.Environment))
		if err != nil {
			httputil.InternalErrorLog(ctx, c, "agent.custom.stage-env", err)
			return
		}
		stagedEnvVersion = envVersion
		if envVersion != proposedEnvVersion {
			validation, err = probe.ValidateBindingCandidate(ctx, binding, envVersion, nil)
			if err != nil {
				if rollbackErr := h.settingsService.RestoreACPEnvState(
					agentID,
					stagedEnvVersion,
					previousEnv,
					previousEnvVersion,
				); rollbackErr != nil {
					err = fmt.Errorf("%w; environment rollback failed: %v", err, rollbackErr)
				}
				httputil.BadRequest(c, fmt.Sprintf(
					"revalidate custom Agent after environment commit failed: AgentID=%q revision=%q: %v",
					agentID,
					revision,
					err,
				))
				return
			}
		}
	}
	if _, err := h.agentCatalog.MutateCustom(ctx, func(agents []model.CustomAgent) ([]model.CustomAgent, error) {
		if conflict := findDefinitionOwner(agents, req.Definition, agentID); conflict != "" {
			return nil, fmt.Errorf("runtime definition is already owned by AgentID %q", conflict)
		}
		for index := range agents {
			if agents[index].AgentID != agentID {
				continue
			}
			agents[index].DisplayName = strings.TrimSpace(req.DisplayName)
			agents[index].IconURL = strings.TrimSpace(req.IconURL)
			agents[index].SupportsHeadlessPrint = req.SupportsHeadlessPrint
			agents[index].Lifecycle = model.AgentLifecycleActive
			agents[index].DeleteCleanupStarted = false
			agents[index].DeleteError = ""
			agents[index].CurrentRevision = revision
			if !hasRevision(agents[index], revision) {
				agents[index].Revisions = append(agents[index].Revisions, model.AgentRuntimeRevision{
					Revision:   revision,
					Definition: req.Definition,
				})
			}
			return agents, nil
		}
		return nil, fmt.Errorf("custom AgentID %q does not exist", agentID)
	}); err != nil {
		if stagedEnvVersion > 0 {
			if rollbackErr := h.settingsService.RestoreACPEnvState(
				agentID,
				stagedEnvVersion,
				previousEnv,
				previousEnvVersion,
			); rollbackErr != nil {
				err = fmt.Errorf("%w; environment rollback failed: %v", err, rollbackErr)
			}
		}
		httputil.Conflict(c, err.Error())
		return
	}
	if err := registerAgentRuntimeBinding(binding); err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.register-runtime", err)
		return
	}
	probe.StoreValidation(validation)
	h.agentExecutions.allow(agentID)
	cacheWarning := ""
	if err := h.acpProbeCache.PersistNow(ctx); err != nil {
		cacheWarning = fmt.Sprintf(
			"custom Agent was updated, but persisting its ACP validation cache failed: %v",
			err,
		)
	}
	item, err := h.catalogItemByID(ctx, agentID)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.update-response", err)
		return
	}
	c.JSON(http.StatusOK, model.CustomAgentResponse{
		Code:    0,
		Agent:   item,
		Warning: cacheWarning,
	})
}

func (h *Handler) RevalidateAgent(ctx context.Context, c *app.RequestContext) {
	agentID := strings.TrimSpace(c.Param("agentId"))
	if agentID == "" {
		httputil.BadRequest(c, "agentId is required")
		return
	}
	var req model.AgentRevalidateRequest
	if len(c.Request.Body()) > 0 {
		if err := c.BindJSON(&req); err != nil {
			httputil.BadRequest(c, "invalid request: "+err.Error())
			return
		}
	}
	entry, found, err := h.agentCatalog.Find(ctx, agentID)
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf("AgentID %q does not exist", agentID)
		}
		httputil.BadRequest(c, err.Error())
		return
	}
	if entry.Source == model.AgentCatalogSourceBuiltin && entry.Builtin != nil && entry.Builtin.Deprecated {
		httputil.BadRequest(c, fmt.Sprintf("AgentID %q is deprecated", agentID))
		return
	}
	if entry.Source == model.AgentCatalogSourceCustom &&
		(entry.Custom == nil || entry.Custom.Lifecycle != model.AgentLifecycleActive) {
		httputil.BadRequest(c, fmt.Sprintf("AgentID %q is not active", agentID))
		return
	}
	binding, found, err := h.agentCatalog.ResolveBinding(ctx, agentID, req.Revision)
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf("AgentID %q revision %q does not exist", agentID, req.Revision)
		}
		httputil.BadRequest(c, err.Error())
		return
	}
	if err := validateInstalled(binding.Definition); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	releaseExecution, acquired := h.agentExecutions.acquireExecution(agentID)
	if !acquired {
		httputil.Conflict(c, fmt.Sprintf(
			"AgentID %q revision %q cannot be revalidated: Agent deletion is in progress",
			agentID,
			binding.Revision,
		))
		return
	}
	defer releaseExecution()
	result, validateErr := probe.ValidateBinding(
		ctx,
		binding,
		h.settingsService.GetACPEnvVersion(agentID),
		nil,
	)
	persistErr := h.acpProbeCache.PersistNow(ctx)
	if validateErr != nil {
		if persistErr != nil {
			validateErr = fmt.Errorf("%w; persist ACP validation result failed: %v", validateErr, persistErr)
		}
		httputil.BadRequest(c, fmt.Sprintf(
			"validate Agent failed: AgentID=%q revision=%q: %v",
			agentID,
			binding.Revision,
			validateErr,
		))
		return
	}
	response := map[string]any{"code": 0, "validation": result}
	if persistErr != nil {
		response["warning"] = fmt.Sprintf(
			"Agent validation succeeded, but persisting the result failed: %v",
			persistErr,
		)
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) CustomAgentDeleteImpact(ctx context.Context, c *app.RequestContext) {
	agentID := strings.TrimSpace(c.Param("agentId"))
	if agentID == "" {
		httputil.BadRequest(c, "agentId is required")
		return
	}
	if _, err := h.customAgentByID(ctx, agentID); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	impact, err := h.agentDeleteImpact(ctx, agentID)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.delete-impact", err)
		return
	}
	c.JSON(http.StatusOK, model.AgentDeleteImpactResponse{Code: 0, Impact: impact})
}

func (h *Handler) DeleteCustomAgent(ctx context.Context, c *app.RequestContext) {
	agentID := strings.TrimSpace(c.Param("agentId"))
	if agentID == "" {
		httputil.BadRequest(c, "agentId is required")
		return
	}
	var req model.AgentDeleteRequest
	if len(c.Request.Body()) > 0 {
		if err := c.BindJSON(&req); err != nil {
			httputil.BadRequest(c, "invalid request: "+err.Error())
			return
		}
	}
	releaseReservation, err := customAgentManagement.reserve("delete", agentID, nil)
	if err != nil {
		httputil.Conflict(c, err.Error())
		return
	}
	defer releaseReservation()
	endDelete, acquired := h.agentExecutions.beginDelete(agentID)
	if !acquired {
		httputil.Conflict(c, fmt.Sprintf("delete AgentID %q is already in progress", agentID))
		return
	}
	keepBlocked := false
	defer func() { endDelete(keepBlocked) }()

	agent, err := h.customAgentByID(ctx, agentID)
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	if agent.DeleteCleanupStarted {
		// A retry after destructive cleanup began must remain blocked even if a
		// later preflight read fails. Restoring active here would publish a
		// partially cleaned Agent.
		keepBlocked = true
	}
	if agent.Lifecycle == model.AgentLifecycleDeleted {
		keepBlocked = true
		impact, _ := h.agentDeleteImpact(ctx, agentID)
		c.JSON(http.StatusOK, model.AgentDeleteResponse{
			Code: 0,
			Result: model.AgentDeleteResult{
				Status: "deleted",
				Impact: impact,
			},
		})
		return
	}
	impact, impactErr := h.agentDeleteImpact(ctx, agentID)
	if impactErr != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.delete-impact-before-delete", impactErr)
		return
	}
	if _, err := h.agentCatalog.MutateCustom(ctx, func(agents []model.CustomAgent) ([]model.CustomAgent, error) {
		for index := range agents {
			if agents[index].AgentID == agentID {
				agents[index].Lifecycle = model.AgentLifecycleDeleting
				return agents, nil
			}
		}
		return nil, fmt.Errorf("custom AgentID %q does not exist", agentID)
	}); err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.mark-deleting", err)
		return
	}
	blocking, blockingErr := h.blockingJobsForAgent(ctx, agentID)
	if blockingErr != nil {
		if !agent.DeleteCleanupStarted {
			restoreErr := h.restoreCustomAgentActive(ctx, agentID)
			if restoreErr != nil {
				blockingErr = fmt.Errorf(
					"%w; restore Agent lifecycle to active failed: %v",
					blockingErr,
					restoreErr,
				)
			}
		}
		httputil.InternalErrorLog(ctx, c, "agent.custom.scan-blocking-jobs", blockingErr)
		return
	}
	var stopResults []model.AgentDeleteStopResult
	if len(blocking) > 0 && req.Force {
		results, stopErrors := h.stopJobsForAgentDeletion(ctx, blocking)
		stopResults = append(stopResults, results...)
		if len(stopErrors) > 0 {
			if !agent.DeleteCleanupStarted {
				if restoreErr := h.restoreCustomAgentActive(ctx, agentID); restoreErr != nil {
					stopErrors = append(stopErrors, "restore active failed: "+restoreErr.Error())
				}
			}
			httputil.Conflict(c, fmt.Sprintf(
				"force delete AgentID %q failed while stopping blocking jobs:\n%s\nall stop results:\n%s",
				agentID,
				strings.Join(stopErrors, "\n"),
				strings.Join(formatAgentDeleteStopResults(stopResults), "\n"),
			))
			return
		}
		blocking, blockingErr = h.blockingJobsForAgent(ctx, agentID)
		if blockingErr != nil {
			if !agent.DeleteCleanupStarted {
				restoreErr := h.restoreCustomAgentActive(ctx, agentID)
				if restoreErr != nil {
					blockingErr = fmt.Errorf(
						"%w; restore Agent lifecycle to active failed: %v",
						blockingErr,
						restoreErr,
					)
				}
			}
			httputil.InternalErrorLog(ctx, c, "agent.custom.rescan-blocking-jobs-after-stop", blockingErr)
			return
		}
	}
	if len(blocking) > 0 {
		if !agent.DeleteCleanupStarted {
			if err := h.restoreCustomAgentActive(ctx, agentID); err != nil {
				httputil.InternalErrorLog(ctx, c, "agent.custom.restore-active", err)
				return
			}
		}
		httputil.Conflict(c, fmt.Sprintf(
			"delete AgentID %q is blocked by non-terminal jobs: %s",
			agentID,
			strings.Join(blocking, ", "),
		))
		return
	}
	h.agentExecutions.waitForExecutions(agentID)
	// A GraphRun snapshot holds an Agent execution lease until the run is
	// durably bound to its Job. Re-scan after all leases drain so a Graph/Schedule
	// that started between the first blocking scan and beginDelete cannot escape
	// the deletion check.
	blocking, blockingErr = h.blockingJobsForAgent(ctx, agentID)
	if blockingErr != nil {
		if !agent.DeleteCleanupStarted {
			restoreErr := h.restoreCustomAgentActive(ctx, agentID)
			if restoreErr != nil {
				blockingErr = fmt.Errorf(
					"%w; restore Agent lifecycle to active failed: %v",
					blockingErr,
					restoreErr,
				)
			}
		}
		httputil.InternalErrorLog(ctx, c, "agent.custom.rescan-blocking-jobs-after-lease", blockingErr)
		return
	}
	if len(blocking) > 0 && req.Force {
		results, stopErrors := h.stopJobsForAgentDeletion(ctx, blocking)
		stopResults = append(stopResults, results...)
		if len(stopErrors) > 0 {
			if !agent.DeleteCleanupStarted {
				if restoreErr := h.restoreCustomAgentActive(ctx, agentID); restoreErr != nil {
					stopErrors = append(stopErrors, "restore active failed: "+restoreErr.Error())
				}
			}
			httputil.Conflict(c, fmt.Sprintf(
				"force delete AgentID %q failed while stopping jobs bound during execution-lease drain:\n%s\nall stop results:\n%s",
				agentID,
				strings.Join(stopErrors, "\n"),
				strings.Join(formatAgentDeleteStopResults(stopResults), "\n"),
			))
			return
		}
		blocking, blockingErr = h.blockingJobsForAgent(ctx, agentID)
		if blockingErr != nil {
			if !agent.DeleteCleanupStarted {
				restoreErr := h.restoreCustomAgentActive(ctx, agentID)
				if restoreErr != nil {
					blockingErr = fmt.Errorf(
						"%w; restore Agent lifecycle to active failed: %v",
						blockingErr,
						restoreErr,
					)
				}
			}
			httputil.InternalErrorLog(ctx, c, "agent.custom.rescan-blocking-jobs-after-forced-stop", blockingErr)
			return
		}
	}
	if len(blocking) > 0 {
		if !agent.DeleteCleanupStarted {
			if err := h.restoreCustomAgentActive(ctx, agentID); err != nil {
				httputil.InternalErrorLog(ctx, c, "agent.custom.restore-active-after-lease", err)
				return
			}
		}
		httputil.Conflict(c, fmt.Sprintf(
			"delete AgentID %q is blocked by jobs created while execution leases were draining: %s",
			agentID,
			strings.Join(blocking, ", "),
		))
		return
	}
	if _, err := h.agentCatalog.MutateCustom(ctx, func(agents []model.CustomAgent) ([]model.CustomAgent, error) {
		for index := range agents {
			if agents[index].AgentID == agentID {
				agents[index].DeleteCleanupStarted = true
				agents[index].DeleteError = ""
				return agents, nil
			}
		}
		return nil, fmt.Errorf("custom AgentID %q does not exist", agentID)
	}); err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.custom.mark-cleanup-started", err)
		return
	}
	keepBlocked = true
	if err := h.settingsService.ClearAgentSettings(agentID); err != nil {
		h.recordAgentDeleteError(ctx, agentID, err)
		httputil.InternalErrorLog(ctx, c, "agent.custom.clear-settings", err)
		return
	}
	if err := h.workspaceService.ClearAgentDefaults(agentID); err != nil {
		h.recordAgentDeleteError(ctx, agentID, err)
		httputil.InternalErrorLog(ctx, c, "agent.custom.clear-workspace-defaults", err)
		return
	}
	if err := h.acpProbeCache.InvalidateAgentAndPersist(ctx, agentID); err != nil {
		h.recordAgentDeleteError(ctx, agentID, err)
		httputil.InternalErrorLog(ctx, c, "agent.custom.persist-cleared-cache", err)
		return
	}
	h.acpAgentService.DeleteByAgentIdentifiers(probe.ACPAgentEnvLookupKeys(agentID))
	unregisterCustomAgentRuntimes(agent)
	if _, err := h.agentCatalog.MutateCustom(ctx, func(agents []model.CustomAgent) ([]model.CustomAgent, error) {
		for index := range agents {
			if agents[index].AgentID == agentID {
				agents[index].Lifecycle = model.AgentLifecycleDeleted
				return agents, nil
			}
		}
		return nil, fmt.Errorf("custom AgentID %q does not exist", agentID)
	}); err != nil {
		h.recordAgentDeleteError(ctx, agentID, err)
		httputil.InternalErrorLog(ctx, c, "agent.custom.finish-delete", err)
		return
	}
	keepBlocked = true
	c.JSON(http.StatusOK, model.AgentDeleteResponse{
		Code: 0,
		Result: model.AgentDeleteResult{
			Status:      "deleted",
			StopResults: stopResults,
			Impact:      impact,
		},
	})
}

func (h *Handler) stopJobsForAgentDeletion(
	ctx context.Context,
	jobIDs []string,
) ([]model.AgentDeleteStopResult, []string) {
	results := make([]model.AgentDeleteStopResult, 0, len(jobIDs))
	var stopErrors []string
	for _, jobID := range jobIDs {
		job, ok := h.jobService.Get(jobID)
		if !ok {
			continue
		}
		if job.Mode == model.JobModeGraph && job.GraphRunID != "" {
			_, err := h.graphService.StopRun(ctx, job.GraphRunID, "forced Agent deletion")
			if err == nil {
				err = h.waitForGraphJobSettled(ctx, job.ID, job.GraphRunID)
			}
			if err != nil {
				stopErrors = append(stopErrors, fmt.Sprintf(
					"job=%s graphRun=%s err=%v",
					job.ID,
					job.GraphRunID,
					err,
				))
				results = append(results, model.AgentDeleteStopResult{
					JobID: job.ID, GraphRunID: job.GraphRunID, Error: err.Error(),
				})
			} else {
				results = append(results, model.AgentDeleteStopResult{
					JobID: job.ID, GraphRunID: job.GraphRunID, Stopped: true,
				})
			}
			continue
		}
		if err := h.stopAndWait(ctx, job); err != nil {
			stopErrors = append(stopErrors, fmt.Sprintf("job=%s err=%v", job.ID, err))
			results = append(results, model.AgentDeleteStopResult{JobID: job.ID, Error: err.Error()})
		} else {
			results = append(results, model.AgentDeleteStopResult{JobID: job.ID, Stopped: true})
		}
	}
	return results, stopErrors
}

const agentDeleteGraphStopTimeout = 30 * time.Second

func (h *Handler) waitForGraphJobSettled(ctx context.Context, jobID, runID string) error {
	waitCtx, cancel := context.WithTimeout(ctx, agentDeleteGraphStopTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	lastRunStatus := model.GraphRunStatus("")
	lastJobStatus := model.JobStatus("")
	for {
		status, err := h.graphService.GetRunStatus(waitCtx, runID)
		if err != nil {
			return fmt.Errorf(
				"load GraphRun while waiting for forced Agent deletion stop failed: job=%s graphRun=%s: %w",
				jobID,
				runID,
				err,
			)
		}
		if status != nil && status.Run != nil {
			lastRunStatus = status.Run.Status
		}
		job, exists := h.jobService.Get(jobID)
		if !exists {
			return fmt.Errorf(
				"bound Job disappeared while waiting for forced Agent deletion stop: job=%s graphRun=%s",
				jobID,
				runID,
			)
		}
		lastJobStatus = job.Status
		jobSettled := job.Status != model.JobStatusPending && job.Status != model.JobStatusRunning
		if graphRunSettled(lastRunStatus) && jobSettled {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf(
				"timed out after %s waiting for GraphRun and bound Job to stop: job=%s jobStatus=%s graphRun=%s graphStatus=%s: %w",
				agentDeleteGraphStopTimeout,
				jobID,
				lastJobStatus,
				runID,
				lastRunStatus,
				waitCtx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func graphRunSettled(status model.GraphRunStatus) bool {
	switch status {
	case model.GraphRunStatusCompleted,
		model.GraphRunStatusFailed,
		model.GraphRunStatusStepStopped,
		model.GraphRunStatusStopped,
		model.GraphRunStatusTimedOut,
		model.GraphRunStatusRecovering,
		model.GraphRunStatusAwaitingInput:
		return true
	default:
		return false
	}
}

func formatAgentDeleteStopResults(results []model.AgentDeleteStopResult) []string {
	lines := make([]string, 0, len(results))
	for _, result := range results {
		switch {
		case result.Error != "":
			lines = append(lines, fmt.Sprintf(
				"job=%s graphRun=%s stopped=false err=%s",
				result.JobID,
				result.GraphRunID,
				result.Error,
			))
		default:
			lines = append(lines, fmt.Sprintf(
				"job=%s graphRun=%s stopped=%t",
				result.JobID,
				result.GraphRunID,
				result.Stopped,
			))
		}
	}
	return lines
}

func (h *Handler) recordAgentDeleteError(ctx context.Context, agentID string, deleteErr error) {
	_, _ = h.agentCatalog.MutateCustom(ctx, func(agents []model.CustomAgent) ([]model.CustomAgent, error) {
		for index := range agents {
			if agents[index].AgentID == agentID {
				agents[index].DeleteError = deleteErr.Error()
				return agents, nil
			}
		}
		return agents, nil
	})
}

func (h *Handler) restoreCustomAgentActive(ctx context.Context, agentID string) error {
	_, err := h.agentCatalog.MutateCustom(ctx, func(agents []model.CustomAgent) ([]model.CustomAgent, error) {
		for index := range agents {
			if agents[index].AgentID == agentID {
				agents[index].Lifecycle = model.AgentLifecycleActive
				return agents, nil
			}
		}
		return nil, fmt.Errorf("custom AgentID %q does not exist", agentID)
	})
	return err
}

func unregisterCustomAgentRuntimes(agent model.CustomAgent) {
	for _, revision := range agent.Revisions {
		pkgacp.UnregisterAgentRuntime(catalog.RuntimeKey(agent.AgentID, revision.Revision))
	}
}

func (h *Handler) reconcileOrphanedAgentSettings(ctx context.Context) error {
	settings, err := h.settingsService.GetSettings()
	if err != nil {
		return fmt.Errorf("read settings for custom Agent reconciliation failed: %w", err)
	}
	customAgents, err := h.agentCatalog.Custom(ctx)
	if err != nil {
		return fmt.Errorf("read custom Agent catalog for settings reconciliation failed: %w", err)
	}
	active := make(map[string]bool, len(customAgents))
	for _, agent := range customAgents {
		if agent.Lifecycle == model.AgentLifecycleActive ||
			(agent.Lifecycle == model.AgentLifecycleDeleting && !agent.DeleteCleanupStarted) {
			active[agent.AgentID] = true
		}
	}
	candidates := make(map[string]bool)
	for agentID := range settings.ACPEnvVars {
		if strings.HasPrefix(agentID, "custom-") {
			candidates[agentID] = true
		}
	}
	for agentID := range settings.ACPEnvVersions {
		if strings.HasPrefix(agentID, "custom-") {
			candidates[agentID] = true
		}
	}
	for agentID := range settings.AgentPrefs {
		if strings.HasPrefix(agentID, "custom-") {
			candidates[agentID] = true
		}
	}
	for _, roleAgentID := range []string{
		agentRoleID(settings.TitleGenerationAgent),
		agentRoleID(settings.GroupReplyAgent),
		imSessionAgentRoleID(settings.IMSessionAgent),
	} {
		if strings.HasPrefix(roleAgentID, "custom-") {
			candidates[roleAgentID] = true
		}
	}
	for agentID := range candidates {
		if active[agentID] {
			continue
		}
		if err := h.settingsService.ClearAgentSettings(agentID); err != nil {
			return fmt.Errorf("clear orphaned custom Agent settings for AgentID %q failed: %w", agentID, err)
		}
	}
	return nil
}

func agentRoleID(config *model.AgentRoleConfig) string {
	if config == nil {
		return ""
	}
	return config.AgentID
}

func imSessionAgentRoleID(config *model.IMSessionAgentConfig) string {
	if config == nil {
		return ""
	}
	return config.AgentID
}

func (h *Handler) reconcileDeletingAgents(ctx context.Context) {
	agents, err := h.agentCatalog.Custom(ctx)
	if err != nil {
		return
	}
	for _, agent := range agents {
		if agent.Lifecycle != model.AgentLifecycleDeleting {
			continue
		}
		if !agent.DeleteCleanupStarted {
			_, _ = h.agentCatalog.MutateCustom(ctx, func(current []model.CustomAgent) ([]model.CustomAgent, error) {
				for index := range current {
					if current[index].AgentID == agent.AgentID {
						current[index].Lifecycle = model.AgentLifecycleActive
						current[index].DeleteError = ""
					}
				}
				return current, nil
			})
			continue
		}
		endDelete, _ := h.agentExecutions.beginDelete(agent.AgentID)
		if err := h.settingsService.ClearAgentSettings(agent.AgentID); err != nil {
			h.recordAgentDeleteError(ctx, agent.AgentID, err)
			endDelete(true)
			continue
		}
		if err := h.workspaceService.ClearAgentDefaults(agent.AgentID); err != nil {
			h.recordAgentDeleteError(ctx, agent.AgentID, err)
			endDelete(true)
			continue
		}
		if err := h.acpProbeCache.InvalidateAgentAndPersist(ctx, agent.AgentID); err != nil {
			h.recordAgentDeleteError(ctx, agent.AgentID, err)
			endDelete(true)
			continue
		}
		h.acpAgentService.DeleteByAgentIdentifiers(probe.ACPAgentEnvLookupKeys(agent.AgentID))
		unregisterCustomAgentRuntimes(agent)
		if _, err := h.agentCatalog.MutateCustom(ctx, func(current []model.CustomAgent) ([]model.CustomAgent, error) {
			for index := range current {
				if current[index].AgentID == agent.AgentID {
					current[index].Lifecycle = model.AgentLifecycleDeleted
					current[index].DeleteError = ""
					return current, nil
				}
			}
			return nil, fmt.Errorf("custom AgentID %q does not exist", agent.AgentID)
		}); err != nil {
			h.recordAgentDeleteError(ctx, agent.AgentID, err)
			endDelete(true)
			continue
		}
		endDelete(true)
	}
}

func (h *Handler) pruneUnreferencedAgentRevisions(ctx context.Context) error {
	referenced := make(map[string]map[string]bool)
	add := func(agentID, revision string) {
		if agentID == "" || revision == "" {
			return
		}
		if referenced[agentID] == nil {
			referenced[agentID] = make(map[string]bool)
		}
		referenced[agentID][revision] = true
	}
	for _, job := range h.jobService.List() {
		for _, queued := range job.MessageQueue {
			add(queued.AgentID, queued.AgentRevision)
		}
		for _, sessionID := range jobAllSessionIDs(job) {
			session, ok := h.lookupSession(sessionID)
			if !ok || session == nil {
				return fmt.Errorf(
					"cannot safely prune Agent revisions because session %q for Job %q could not be loaded",
					sessionID,
					job.ID,
				)
			}
			if session.AgentID != "" && session.AgentRevision != "" {
				add(session.AgentID, session.AgentRevision)
				continue
			}
			if binding, found, err := h.agentCatalog.ResolveLegacyBinding(ctx, session.Type); err == nil && found {
				add(binding.AgentID, binding.Revision)
			}
		}
		if job.GraphRunID == "" {
			continue
		}
		status, err := h.graphService.GetRunStatus(ctx, job.GraphRunID)
		if err != nil {
			return fmt.Errorf(
				"cannot safely prune Agent revisions because GraphRun %q for Job %q could not be loaded: %w",
				job.GraphRunID,
				job.ID,
				err,
			)
		}
		if status == nil || status.Run == nil {
			return fmt.Errorf(
				"cannot safely prune Agent revisions because GraphRun %q for Job %q has no run snapshot",
				job.GraphRunID,
				job.ID,
			)
		}
		run := status.Run
		if run.Status == model.GraphRunStatusCompleted {
			continue
		}
		for _, snapshot := range run.BaseSnapshot.AgentSnapshots {
			add(snapshot.AgentID, snapshot.Revision)
		}
		for _, version := range run.Versions {
			for _, snapshot := range version.AgentSnapshots {
				add(snapshot.AgentID, snapshot.Revision)
			}
		}
	}
	removed, err := h.agentCatalog.PruneUnreferencedCustomRevisions(ctx, referenced)
	if err != nil {
		return fmt.Errorf("prune unreferenced custom Agent revisions failed: %w", err)
	}
	builtinRemoved, err := h.agentCatalog.PruneUnreferencedBuiltinRevisions(ctx, referenced)
	if err != nil {
		return fmt.Errorf("prune unreferenced built-in Agent revisions failed: %w", err)
	}
	removed = append(removed, builtinRemoved...)
	if len(removed) == 0 {
		return nil
	}
	if err := h.acpProbeCache.InvalidateBindingsAndPersist(ctx, removed); err != nil {
		return fmt.Errorf("persist ACP cache after pruning Agent revisions failed: %w", err)
	}
	for _, binding := range removed {
		pkgacp.UnregisterAgentRuntime(binding.RuntimeKey)
	}
	return nil
}

func bindCustomAgentRequest(c *app.RequestContext, req *model.CustomAgentUpsertRequest) bool {
	if err := c.BindJSON(req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return false
	}
	return true
}

func validateCustomAgentRequest(req model.CustomAgentUpsertRequest) error {
	if strings.TrimSpace(req.DisplayName) == "" {
		return fmt.Errorf("display_name is required")
	}
	if strings.TrimSpace(req.Definition.Bin) == "" {
		return fmt.Errorf("definition.bin is required")
	}
	if strings.TrimSpace(req.Definition.ACPProgram) == "" {
		return fmt.Errorf("definition.acp_program is required")
	}
	return nil
}

func validateCustomRuntime(binding model.AgentRuntimeBinding) error {
	return validateInstalled(binding.Definition)
}

func validateInstalled(definition model.AgentRuntimeDefinition) error {
	status := (agentinstall.Checker{}).Check(agentinstall.Definition{
		Bin:        definition.Bin,
		ACPProgram: definition.ACPProgram,
	})
	if !status.Installed {
		return fmt.Errorf("custom Agent is not installed: %s", status.Error)
	}
	return nil
}

func toACPEnv(entries []model.AgentEnvironmentItem) []pkgacp.EnvVar {
	result := make([]pkgacp.EnvVar, 0, len(entries))
	for _, entry := range entries {
		if entry.Enabled && strings.TrimSpace(entry.Key) != "" {
			result = append(result, pkgacp.EnvVar{Key: strings.TrimSpace(entry.Key), Value: entry.Value})
		}
	}
	return result
}

func toSettingsEnv(entries []model.AgentEnvironmentItem) []model.ACPEnvVarEntry {
	result := make([]model.ACPEnvVarEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, model.ACPEnvVarEntry{
			Key:     strings.TrimSpace(entry.Key),
			Value:   entry.Value,
			Enabled: entry.Enabled,
		})
	}
	return result
}

func settingsEnvToACP(entries []model.ACPEnvVarEntry) []pkgacp.EnvVar {
	result := make([]pkgacp.EnvVar, 0, len(entries))
	for _, entry := range entries {
		if entry.Enabled && strings.TrimSpace(entry.Key) != "" {
			result = append(result, pkgacp.EnvVar{
				Key:   strings.TrimSpace(entry.Key),
				Value: entry.Value,
			})
		}
	}
	return result
}

func newCustomAgentID() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate custom AgentID failed: %w", err)
	}
	return "custom-" + hex.EncodeToString(random[:]), nil
}

func (h *Handler) customAgentByID(ctx context.Context, agentID string) (model.CustomAgent, error) {
	entry, found, err := h.agentCatalog.Find(ctx, agentID)
	if err != nil {
		return model.CustomAgent{}, err
	}
	if !found || entry.Source != model.AgentCatalogSourceCustom || entry.Custom == nil {
		return model.CustomAgent{}, fmt.Errorf("custom AgentID %q does not exist", agentID)
	}
	return *entry.Custom, nil
}

func (h *Handler) catalogItemByID(ctx context.Context, agentID string) (model.AgentCatalogItem, error) {
	items, err := h.agentCatalog.ListItems(ctx)
	if err != nil {
		return model.AgentCatalogItem{}, err
	}
	for _, item := range items {
		if item.AgentID == agentID {
			return item, nil
		}
	}
	return model.AgentCatalogItem{}, fmt.Errorf("AgentID %q is not visible in the management catalog", agentID)
}

type definitionConflict struct {
	AgentID     string
	DisplayName string
	Lifecycle   model.AgentLifecycle
}

func findDefinitionOwner(agents []model.CustomAgent, definition model.AgentRuntimeDefinition, exclude string) string {
	conflict := findDefinitionConflict(agents, definition, exclude)
	return conflict.AgentID
}

func findDefinitionConflict(agents []model.CustomAgent, definition model.AgentRuntimeDefinition, exclude string) definitionConflict {
	for _, builtin := range catalog.BuiltinSnapshot() {
		if catalog.DefinitionEqual(builtin.RuntimeDefinition(), definition) {
			return definitionConflict{AgentID: builtin.AgentID, DisplayName: builtin.DisplayName, Lifecycle: model.AgentLifecycleActive}
		}
		for _, identifier := range builtin.HistoricalIdentifiers {
			if identifier.Kind != catalog.IdentifierKindACPCommand {
				continue
			}
			historical, err := catalog.LegacyDefinition(identifier.Value)
			if err != nil {
				continue
			}
			historical.Bin = builtin.Bin
			if catalog.DefinitionEqual(historical, definition) {
				return definitionConflict{AgentID: builtin.AgentID, DisplayName: builtin.DisplayName, Lifecycle: model.AgentLifecycleActive}
			}
		}
	}
	for _, agent := range agents {
		if agent.AgentID == exclude {
			continue
		}
		binding, err := catalog.BindingForCustom(agent, agent.CurrentRevision)
		if err == nil && catalog.DefinitionEqual(binding.Definition, definition) {
			return definitionConflict{AgentID: agent.AgentID, DisplayName: agent.DisplayName, Lifecycle: agent.Lifecycle}
		}
	}
	return definitionConflict{}
}

func hasRevision(agent model.CustomAgent, revision string) bool {
	for _, candidate := range agent.Revisions {
		if candidate.Revision == revision {
			return true
		}
	}
	return false
}

func (h *Handler) agentDeleteImpact(ctx context.Context, agentID string) (model.AgentDeleteImpact, error) {
	impact := model.AgentDeleteImpact{AgentID: agentID}
	settings, err := h.settingsService.GetSettings()
	if err != nil {
		return impact, fmt.Errorf("read Agent settings impact failed: %w", err)
	}
	if len(settings.ACPEnvVars[agentID]) > 0 || settings.ACPEnvVersions[agentID] > 0 {
		impact.ClearedSettings = append(impact.ClearedSettings, "environment")
	}
	if _, exists := settings.AgentPrefs[agentID]; exists {
		impact.ClearedSettings = append(impact.ClearedSettings, "agent_defaults")
	}
	if settings.TitleGenerationAgent != nil && settings.TitleGenerationAgent.AgentID == agentID {
		impact.ClearedSettings = append(impact.ClearedSettings, "title_generation_agent")
	}
	if settings.GroupReplyAgent != nil && settings.GroupReplyAgent.AgentID == agentID {
		impact.ClearedSettings = append(impact.ClearedSettings, "group_reply_agent")
	}
	if settings.IMSessionAgent != nil && settings.IMSessionAgent.AgentID == agentID {
		impact.ClearedSettings = append(impact.ClearedSettings, "im_session_agent")
	}

	workflows, warnings, err := h.graphService.ListWorkflows(ctx)
	if err != nil {
		return impact, fmt.Errorf("list GraphWorkflows for Agent deletion impact failed: %w", err)
	}
	if len(warnings) > 0 {
		var warningLines []string
		for _, warning := range warnings {
			warningLines = append(warningLines, fmt.Sprintf("%s: %s", warning.File, warning.Error))
		}
		return impact, fmt.Errorf(
			"cannot calculate complete Agent deletion impact because GraphWorkflow files failed to load:\n%s",
			strings.Join(warningLines, "\n"),
		)
	}
	workflowUsesAgent := make(map[string]bool)
	for _, workflow := range workflows {
		if workflow == nil {
			continue
		}
		for _, node := range workflow.Config.Nodes {
			matches, err := h.referenceMatchesAgent(ctx, "", node.Config.AgentType, agentID)
			if err != nil {
				return impact, fmt.Errorf(
					"resolve GraphWorkflow %q node %q Agent reference failed: %w",
					workflow.ID,
					node.ID,
					err,
				)
			}
			if matches {
				workflowUsesAgent[workflow.ID] = true
				impact.RetainedWorkflows = append(impact.RetainedWorkflows, workflow.ID)
				break
			}
		}
	}
	schedules, err := h.scheduleService.List(ctx)
	if err != nil {
		return impact, fmt.Errorf("list schedules for Agent deletion impact failed: %w", err)
	}
	for _, schedule := range schedules {
		if schedule != nil && workflowUsesAgent[schedule.GraphWorkflowID] {
			impact.RetainedSchedules = append(impact.RetainedSchedules, schedule.ID)
		}
	}

	jobSeen := make(map[string]bool)
	sessionSeen := make(map[string]bool)
	for _, job := range h.jobService.List() {
		usesAgent, matchingSessions, err := h.jobReferencesAgent(ctx, job, agentID)
		if err != nil {
			return impact, err
		}
		for _, sessionID := range matchingSessions {
			if !sessionSeen[sessionID] {
				sessionSeen[sessionID] = true
				impact.RetainedSessions = append(impact.RetainedSessions, sessionID)
			}
		}
		if usesAgent && !jobSeen[job.ID] {
			jobSeen[job.ID] = true
			impact.RetainedJobs = append(impact.RetainedJobs, job.ID)
		}
	}
	blocking, err := h.blockingJobsForAgent(ctx, agentID)
	if err != nil {
		return impact, err
	}
	impact.BlockingJobIDs = blocking
	return impact, nil
}

func (h *Handler) blockingJobsForAgent(ctx context.Context, agentID string) ([]string, error) {
	var blocking []string
	for _, job := range h.jobService.List() {
		if job.Status != model.JobStatusPending && job.Status != model.JobStatusRunning {
			continue
		}
		used, _, err := h.jobReferencesAgent(ctx, job, agentID)
		if err != nil {
			return nil, err
		}
		if used {
			blocking = append(blocking, job.ID)
		}
	}
	return blocking, nil
}

func (h *Handler) jobReferencesAgent(
	ctx context.Context,
	job *model.Job,
	agentID string,
) (bool, []string, error) {
	if job == nil {
		return false, nil, nil
	}
	usesAgent := false
	var matchingSessions []string
	for _, sessionID := range jobAllSessionIDs(job) {
		session, ok := h.lookupSession(sessionID)
		if !ok || session == nil {
			return false, nil, fmt.Errorf(
				"cannot confirm Agent references because session %q for Job %q could not be loaded",
				sessionID,
				job.ID,
			)
		}
		matches, err := h.referenceMatchesAgent(ctx, session.AgentID, session.Type, agentID)
		if err != nil {
			return false, nil, fmt.Errorf(
				"resolve session %q Agent reference for Job %q failed: %w",
				sessionID,
				job.ID,
				err,
			)
		}
		if matches {
			usesAgent = true
			matchingSessions = append(matchingSessions, sessionID)
		}
	}
	if usesAgent || job.GraphRunID == "" {
		return usesAgent, matchingSessions, nil
	}

	status, err := h.graphService.GetRunStatus(ctx, job.GraphRunID)
	if err != nil {
		return false, nil, fmt.Errorf(
			"cannot confirm Agent references because GraphRun %q for Job %q could not be loaded: %w",
			job.GraphRunID,
			job.ID,
			err,
		)
	}
	if status == nil || status.Run == nil {
		return false, nil, fmt.Errorf(
			"cannot confirm Agent references because GraphRun %q for Job %q has no run snapshot",
			job.GraphRunID,
			job.ID,
		)
	}
	checkSnapshots := func(
		location string,
		snapshots map[string]model.GraphAgentSnapshot,
	) (bool, error) {
		for nodeID, snapshot := range snapshots {
			matches, err := h.referenceMatchesAgent(ctx, snapshot.AgentID, snapshot.AgentType, agentID)
			if err != nil {
				return false, fmt.Errorf(
					"resolve GraphRun %q %s node %q Agent reference failed: %w",
					job.GraphRunID,
					location,
					nodeID,
					err,
				)
			}
			if matches {
				return true, nil
			}
		}
		return false, nil
	}
	if matches, err := checkSnapshots("base snapshot", status.Run.BaseSnapshot.AgentSnapshots); err != nil {
		return false, nil, err
	} else if matches {
		return true, matchingSessions, nil
	}
	for _, version := range status.Run.Versions {
		matches, err := checkSnapshots(
			fmt.Sprintf("version %d", version.Version),
			version.AgentSnapshots,
		)
		if err != nil {
			return false, nil, err
		}
		if matches {
			return true, matchingSessions, nil
		}
	}
	return false, matchingSessions, nil
}

func (h *Handler) referenceMatchesAgent(
	ctx context.Context,
	stableID string,
	legacyReference string,
	agentID string,
) (bool, error) {
	if stableID != "" {
		return stableID == agentID, nil
	}
	if legacyReference == "" {
		return false, nil
	}
	resolved, found, err := h.agentCatalog.Resolve(ctx, legacyReference)
	if err != nil {
		return false, fmt.Errorf("resolve legacy Agent reference %q failed: %w", legacyReference, err)
	}
	return found && resolved.AgentID == agentID, nil
}
