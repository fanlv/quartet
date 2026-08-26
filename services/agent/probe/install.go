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
	ErrAgentNotInstalled = errors.New("built-in agent is not installed")
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
	return s.runBuiltinAgentInstall(ctx, agentID, false)
}

// UpgradeBuiltinAgent re-runs the catalog-declared install steps for an
// installed built-in Agent, then rechecks and validates the resulting ACP
// runtime. The client still supplies only AgentID; commands remain entirely
// controlled by the built-in catalog.
func (s *CacheService) UpgradeBuiltinAgent(ctx context.Context, agentID string) (*model.AgentInstallResult, error) {
	return s.runBuiltinAgentInstall(ctx, agentID, true)
}

func (s *CacheService) runBuiltinAgentInstall(ctx context.Context, agentID string, upgrade bool) (*model.AgentInstallResult, error) {
	def, ok := FindACPAgentByID(agentID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAgentID, agentID)
	}
	if def.Deprecated {
		return nil, fmt.Errorf("%w: %q (%s)", ErrAgentDeprecated, agentID, def.DisplayName)
	}
	platform := agentinstall.CurrentPlatform()
	if (!upgrade && !def.Install.AutoInstallable(platform)) || (upgrade && !def.Install.AutoUpgradeable(platform)) {
		return nil, fmt.Errorf("%w: %q (%s): %s", ErrManualInstallOnly, agentID, def.DisplayName, def.Install.Instructions)
	}

	checker := agentinstall.Checker{}
	definition := agentinstall.Definition{Bin: def.Bin, ACPProgram: def.ACPProgram}
	result := &model.AgentInstallResult{AgentID: agentID}
	precheck := checker.Check(definition)
	if upgrade && !precheck.Installed {
		return nil, fmt.Errorf("%w: %q (%s): %s", ErrAgentNotInstalled, agentID, def.DisplayName, precheck.Error)
	}

	// A regular install skips execution when the Agent is already present and
	// goes straight to revalidation. An explicit upgrade always re-runs the
	// catalog-controlled steps.
	if upgrade || !precheck.Installed {
		stepsToRun := def.Install.StepsForInstall(platform)
		if upgrade {
			stepsToRun = def.Install.StepsForUpgrade(platform)
		}
		steps, err := agentinstall.RunSteps(ctx, stepsToRun, installStepTimeout)
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
	for _, step := range result.Steps {
		if step.ExitCode == 0 && !step.TimedOut && step.Error == "" {
			continue
		}
		result.InstallError = fmt.Sprintf(
			"preset install step failed: %s (exit_code=%d timed_out=%t error=%s)",
			step.Display,
			step.ExitCode,
			step.TimedOut,
			step.Error,
		)
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

// UninstallBuiltinAgent runs the platform-specific automatic uninstall flow
// for agentID, then re-runs the unified installation check.
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
	platform := agentinstall.CurrentPlatform()
	if !def.Install.AutoUninstallable(platform) {
		return nil, fmt.Errorf("%w: %q (%s)", ErrNotUninstallable, agentID, def.DisplayName)
	}

	checker := agentinstall.Checker{}
	definition := agentinstall.Definition{Bin: def.Bin, ACPProgram: def.ACPProgram}
	result := &model.AgentInstallResult{AgentID: agentID}

	steps, err := agentinstall.RunSteps(ctx, def.Install.StepsForUninstall(platform), installStepTimeout)
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
	if recheck.Installed {
		result.InstallError = fmt.Sprintf(
			"Agent remains installed after uninstall: bin=%q ACP program=%q",
			recheck.Bin.ResolvedPath,
			recheck.ACPProgram.ResolvedPath,
		)
	}
	s.InvalidateAgent(agentID)
	if err := s.PersistNow(ctx); err != nil {
		logger.Warnf(ctx, "[probe] persist ACP cache after uninstall failed: agent=%s err=%v", agentID, err)
	}
	return result, nil
}
