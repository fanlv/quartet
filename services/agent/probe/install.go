package probe

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/agent/catalog"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/types/model"
)

// installStepTimeout bounds every single preset install step so a hanging
// installer can't block the request indefinitely.
const installStepTimeout = 10 * time.Minute

// Sentinel errors of the built-in install flow; the HTTP layer maps them with
// errors.Is. Messages are wrapped with full context before reaching users.
var (
	ErrUnknownAgentID    = errors.New("unknown built-in agent id")
	ErrAgentDeprecated   = errors.New("built-in agent is deprecated")
	ErrManualInstallOnly = errors.New("built-in agent only supports manual installation")
	ErrNotUninstallable  = errors.New("built-in agent does not support automatic uninstall")
)

// ValidateAgent forces a live ACP validation for the given agent command and
// updates the in-memory selector cache on success. Unlike the background
// refresh fan-out it never consults the failure backoff — explicit
// validation always probes. The probe itself is bounded by acpProbeTimeout.
func ValidateAgent(ctx context.Context, binding model.AgentRuntimeBinding, envVersion int64) error {
	_, err := ValidateBinding(ctx, binding, envVersion, nil)
	return err
}

// InstallBuiltinAgent runs the preset install flow declared by the catalog
// entry for agentID: execute the preset steps (skipped when already
// installed), re-run the unified installation check, then — only when
// installed — run a full ACP validation. The returned result carries every
// step's complete output, the recheck outcome, and the validation outcome;
// a failing install or validation is reported inside the result, not as an
// error. Errors are reserved for requests that cannot start an install at
// all (unknown/deprecated/manual-only agent, another install in flight).
func (s *CacheService) InstallBuiltinAgent(ctx context.Context, agentID string) (*model.AgentInstallResult, error) {
	def, ok := FindACPAgentByID(agentID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAgentID, agentID)
	}
	if def.Deprecated {
		return nil, fmt.Errorf("%w: %q (%s)", ErrAgentDeprecated, agentID, def.DisplayName)
	}
	if !def.Install.AutoInstallable() {
		return nil, fmt.Errorf("%w: %q (%s): %s", ErrManualInstallOnly, agentID, def.DisplayName, def.Install.Instructions)
	}

	checker := agentinstall.Checker{}
	definition := agentinstall.Definition{Bin: def.Bin, ACPProgram: def.ACPProgram}
	result := &model.AgentInstallResult{AgentID: agentID}

	// Already-installed entries skip execution and go straight to
	// revalidation, which also covers the "installed but never probed" case.
	if pre := checker.Check(definition); !pre.Installed {
		steps, err := agentinstall.RunSteps(ctx, def.Install.Steps, installStepTimeout)
		if err != nil {
			return nil, err
		}
		for _, step := range steps {
			result.Steps = append(result.Steps, model.AgentInstallStepResult{
				Display:    step.Display,
				Stdout:     step.Stdout,
				Stderr:     step.Stderr,
				ExitCode:   step.ExitCode,
				TimedOut:   step.TimedOut,
				Error:      step.Error,
				DurationMs: step.DurationMs,
			})
		}
	}

	// Unified recheck after the steps: a zero exit status with the binaries
	// still missing from the backend PATH still counts as not installed.
	recheck := checker.Check(definition)
	result.Installed = recheck.Installed
	if !recheck.Installed {
		result.InstallError = recheck.Error
		return result, nil
	}

	validation := &model.AgentValidationResult{}
	binding := catalog.BindingForBuiltin(def)
	if err := ValidateAgent(ctx, binding, agentEnvVersion(def.AgentID)); err != nil {
		validation.Error = err.Error()
	} else {
		validation.OK = true
	}
	if err := s.PersistNow(ctx); err != nil {
		logger.Warnf(ctx, "[probe] persist ACP cache after install validation failed: agent=%s err=%v", agentID, err)
	}
	result.Validation = validation
	return result, nil
}

// UninstallBuiltinAgent runs the automatic uninstall flow for agentID (only
// npm-method built-ins are uninstallable): execute the derived
// `npm uninstall -g <pkg>` steps, then re-run the unified installation check.
// The returned result carries every step's complete output and the recheck
// outcome — a successful uninstall leaves Installed false. Unlike install,
// no ACP validation runs afterward. Errors are reserved for requests that
// cannot start an uninstall at all (unknown agent, not uninstallable, another
// install/uninstall in flight).
func (s *CacheService) UninstallBuiltinAgent(ctx context.Context, agentID string) (*model.AgentInstallResult, error) {
	def, ok := FindACPAgentByID(agentID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAgentID, agentID)
	}
	if !def.Install.AutoUninstallable() {
		return nil, fmt.Errorf("%w: %q (%s)", ErrNotUninstallable, agentID, def.DisplayName)
	}

	checker := agentinstall.Checker{}
	definition := agentinstall.Definition{Bin: def.Bin, ACPProgram: def.ACPProgram}
	result := &model.AgentInstallResult{AgentID: agentID}

	steps, err := agentinstall.RunSteps(ctx, def.Install.UninstallSteps(), installStepTimeout)
	if err != nil {
		return nil, err
	}
	for _, step := range steps {
		result.Steps = append(result.Steps, model.AgentInstallStepResult{
			Display:    step.Display,
			Stdout:     step.Stdout,
			Stderr:     step.Stderr,
			ExitCode:   step.ExitCode,
			TimedOut:   step.TimedOut,
			Error:      step.Error,
			DurationMs: step.DurationMs,
		})
	}

	// Recheck after removal: a zero exit status with the binaries still on the
	// backend PATH still counts as installed (uninstall did not take effect).
	recheck := checker.Check(definition)
	result.Installed = recheck.Installed
	if err := s.PersistNow(ctx); err != nil {
		logger.Warnf(ctx, "[probe] persist ACP cache after uninstall failed: agent=%s err=%v", agentID, err)
	}
	return result, nil
}
