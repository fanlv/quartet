package graph

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// Run-control service methods (step 16) and resume support (step 15).

// StopRun hard-stops a running GraphRun.
func (s *serviceImpl) StopRun(ctx context.Context, runID, reason string) (*model.GraphRun, error) {
	return s.signalAndSnapshot(ctx, runID, controlSignal{kind: ctrlHardStop, reason: orDefault(reason, "hard stopped by user")})
}

// StepStopRun freezes the current ready batch and stops after it.
func (s *serviceImpl) StepStopRun(ctx context.Context, runID, reason string) (*model.GraphRun, error) {
	return s.signalAndSnapshot(ctx, runID, controlSignal{kind: ctrlStepStop, reason: orDefault(reason, "step-stopped by user")})
}

// CancelStopRun cancels a pending step-stop that has not yet settled,
// releasing the held dispatch frontier and returning the run to running.
func (s *serviceImpl) CancelStopRun(ctx context.Context, runID, reason string) (*model.GraphRun, error) {
	return s.signalAndSnapshot(ctx, runID, controlSignal{kind: ctrlCancelStop, reason: orDefault(reason, "stop cancelled by user")})
}

// signalAndSnapshot delivers a control signal then returns the current run
// snapshot. The actual state transition happens asynchronously in the
// scheduler goroutine.
func (s *serviceImpl) signalAndSnapshot(ctx context.Context, runID string, sig controlSignal) (*model.GraphRun, error) {
	if _, err := s.runRepo.GetRun(ctx, runID); err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	if err := s.sendControl(runID, sig); err != nil {
		return nil, err
	}
	return s.runRepo.GetRun(ctx, runID)
}

// resumableStatuses are the GraphRun statuses from which ResumeRun is allowed.
// awaitingInput is included: a parked clarify run is a resumable terminal (its
// scheduler has exited and state is persisted), continued via ContinueRun which
// shares this resume kernel — the continue finalizes the clarify instances on
// the way through seedResume (finalizeAwaitingClarify).
func isResumableStatus(st model.GraphRunStatus) bool {
	switch st {
	case model.GraphRunStatusFailed, model.GraphRunStatusStepStopped,
		model.GraphRunStatusStopped,
		model.GraphRunStatusTimedOut, model.GraphRunStatusRecovering,
		model.GraphRunStatusAwaitingInput:
		return true
	default:
		return false
	}
}

// isInFlightStatus reports whether a run is actively scheduling (and so cannot
// be deleted). Recovering is deliberately excluded: it is a static resumable
// state after crash recovery, with no live scheduler to accept controls.
func isInFlightStatus(st model.GraphRunStatus) bool {
	switch st {
	case model.GraphRunStatusRunning, model.GraphRunStatusStepStopping:
		return true
	default:
		return false
	}
}

// ResumeRun relaunches a resumable GraphRun. It resets the resettable terminal
// instances (failed/interrupted) and their downstream, keeps succeeded/skipped
// instances, then re-launches the scheduler in resume mode.
func (s *serviceImpl) ResumeRun(ctx context.Context, runID string, runner Runner, jobs JobStateSink) (*model.GraphRun, error) {
	if runner == nil {
		return nil, ErrGraphRunnerMissing
	}
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	if !isResumableStatus(run.Status) {
		return nil, fmt.Errorf("%w: status=%s", ErrGraphRunNotResumable, run.Status)
	}
	return s.relaunchResumableRun(ctx, run, runner, jobs)
}

// ContinueRun continues a GraphRun parked at「等待人工」(§5 讨论完成/续跑): the user
// finished discussing in the clarify node session(s) and clicked「讨论完成」. It
// shares the resume kernel with ResumeRun — relaunchResumableRun re-enters the
// scheduler in resume mode, and seedResume's finalizeAwaitingClarify captures
// each clarify's 结论 (the session's last assistant message), flips it to
// succeeded, and resolves its held out-edges so the DAG continues. Guarded to the
// awaitingInput status specifically so a misfired continue on a failed/stopped
// run returns a clear error rather than silently resuming.
func (s *serviceImpl) ContinueRun(ctx context.Context, runID string, runner Runner, jobs JobStateSink) (*model.GraphRun, error) {
	if runner == nil {
		return nil, ErrGraphRunnerMissing
	}
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	if run.Status != model.GraphRunStatusAwaitingInput {
		return nil, fmt.Errorf("%w: continue requires awaitingInput, status=%s", ErrGraphRunNotResumable, run.Status)
	}
	return s.relaunchResumableRun(ctx, run, runner, jobs)
}

// relaunchResumableRun is the shared resume kernel for ResumeRun and ContinueRun:
// it resets the resettable terminal instances (failed/interrupted) and their
// downstream, keeps succeeded/skipped (and awaitingInput) instances, persists the
// post-reset state, then re-launches the scheduler in resume mode. The caller is
// responsible for status validation; `run` is the already-loaded run.
func (s *serviceImpl) relaunchResumableRun(ctx context.Context, run *model.GraphRun, runner Runner, jobs JobStateSink) (*model.GraphRun, error) {
	runID := run.ID
	instances, err := s.runRepo.GetInstances(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load instances failed: %w", err)
	}
	edges, err := s.runRepo.GetEdges(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load edges failed: %w", err)
	}
	vars, err := s.runRepo.GetVariables(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load variables failed: %w", err)
	}

	cfg := effectiveConfig(run)
	var loopState map[string]model.GraphLoopState
	if run.Resume != nil {
		loopState = run.Resume.LoopState
	}
	rb := newResumeBuilder(cfg, instances, edges, vars, loopState)
	rb.resetResettable()

	// Preserve the sessions of instances the reset removed so the Chat sidebar
	// keeps listing prior-attempt conversations across the resume. A re-run that
	// reaches the same instance key overwrites the live instance; the archive is
	// only consulted for keys no longer present live (frontend merges live-first).
	if len(rb.archived) > 0 {
		if run.ArchivedInstances == nil {
			run.ArchivedInstances = map[string]model.GraphInstanceState{}
		}
		maps.Copy(run.ArchivedInstances, rb.archived)
	}

	if err := s.runRepo.SaveInstances(ctx, runID, rb.instances); err != nil {
		return nil, err
	}
	if err := s.runRepo.SaveEdges(ctx, runID, rb.edges); err != nil {
		return nil, err
	}
	if err := s.runRepo.SaveVariables(ctx, runID, rb.vars); err != nil {
		return nil, err
	}
	// Clear the frozen batch / step flags so a fresh scheduler does not re-freeze.
	if run.Resume != nil {
		run.Resume.FrozenBatch = nil
	}
	run.Status = model.GraphRunStatusRunning
	run.LastError = nil
	if run.Progress != nil {
		run.Progress.LastError = ""
	}
	run.UpdatedAt = time.Now()
	if err := s.runRepo.SaveRun(ctx, run); err != nil {
		return nil, err
	}

	go s.runGraph(context.Background(), runID, runner, jobs, true)
	return run, nil
}

// DeleteRun deletes a non-in-flight GraphRun and clears the bound Job linkage.
func (s *serviceImpl) DeleteRun(ctx context.Context, runID string, jobs JobStateSink) error {
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return graphRunLoadError(runID, err)
	}
	if isInFlightStatus(run.Status) {
		return fmt.Errorf("%w: status=%s", ErrGraphRunInFlight, run.Status)
	}
	if err := s.runRepo.DeleteRun(ctx, runID); err != nil {
		return err
	}
	// Free the in-memory event buffer (if any reader is still attached, Close
	// wakes it so its SSE handler exits). The run's status is already terminal
	// (in-flight runs are rejected above), so no producer is publishing.
	s.removeBuffer(runID)
	if jobs != nil && run.JobID != "" {
		if err := jobs.ClearGraphRunLinkage(ctx, run.JobID, runID); err != nil {
			logger.Warnf(ctx, "[graph] clear job graph-run linkage failed: job=%s run=%s err=%v", run.JobID, runID, err)
		}
	}
	return nil
}

// sortInstanceKeysByDepth orders instance-key strings by iteration depth so
// parent loop scopes are rebuilt before their children on resume.
func sortInstanceKeysByDepth(keys []model.GraphInstanceKey) {
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i].Iterations) != len(keys[j].Iterations) {
			return len(keys[i].Iterations) < len(keys[j].Iterations)
		}
		return instanceKeyString(keys[i]) < instanceKeyString(keys[j])
	})
}
