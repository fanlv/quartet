package job

import (
	"context"
	"fmt"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// LoopConfig editing during a job's lifetime comes in two flavours, split by
// whether the job is currently running:
//
//   - ReplaceLoopConfig (job NOT running): swap the whole flow. Structure may
//     change freely — Continue re-reads job.LoopConfig from scratch, so the
//     only work is recomputing the progress denominator and discarding resume /
//     result bookkeeping that no longer points at a real step.
//   - UpdateRunningStepFields (job running): the runLoop goroutine holds the
//     flow tree as a snapshot and walks it by index, so the structure must NOT
//     change underneath it. We validate the editable fields, verify the
//     structure is identical, persist a copy, then commit the per-step editable
//     fields onto the live flow (save-then-commit, so a save failure leaves the
//     running loop untouched). The running loop picks the new values up via
//     liveStepFields just before each step runs.

// ReplaceLoopConfig swaps a non-running job's LoopConfig wholesale and
// reconciles its progress bookkeeping against the new flow. See the Service
// interface for the contract.
func (s *serviceImpl) ReplaceLoopConfig(ctx context.Context, jobID string, cfg *model.LoopConfig) (*model.JobProgress, error) {
	if cfg == nil {
		return nil, ErrNoLoopConfig
	}
	// Normalize + validate outside the locks (pure, no shared state).
	model.MigrateLoopConfig(cfg)
	if len(cfg.Flow) == 0 {
		return nil, fmt.Errorf("loopConfig.flow must not be empty")
	}
	if err := model.ValidateFlow(cfg.Flow, 0); err != nil {
		return nil, err
	}

	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return nil, ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return nil, ErrJobDeleted
	}
	if existing.Status == model.JobStatusRunning {
		s.mu.Unlock()
		return nil, ErrJobRunning
	}
	cp := existing.DeepCopy()
	s.mu.Unlock()

	if cp.LoopConfig == nil {
		cp.LoopConfig = &model.LoopConfig{}
	}
	cp.LoopConfig.Flow = cfg.Flow
	// Flow is now the canonical form — drop any legacy flat fields so a later
	// MigrateLoopConfig can't resurrect a stale Rounds list over the new tree.
	cp.LoopConfig.Rounds = nil
	cp.LoopConfig.IterationCount = 0
	// Variables: only overwrite when the caller supplied a map. The editor does
	// not manage runtime-injected builtins (_current_time, jobId, …); leaving
	// the existing map intact preserves user-defined template vars, and Start /
	// Continue re-inject the builtins at runLoop entry regardless.
	if cfg.Variables != nil {
		cp.LoopConfig.Variables = cfg.Variables
	}
	reconcileProgressToFlow(cp, cfg.Flow)
	cp.UpdatedAt = time.Now()

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return nil, err
	}

	// Disk committed. Mirror to in-memory state, gated on the pointer not
	// having been swapped by a concurrent reload (same guard as updateJobField).
	s.mu.Lock()
	if cur, ok := s.jobs[jobID]; ok && cur == existing {
		existing.LoopConfig = cp.LoopConfig
		existing.Progress = cp.Progress
		existing.Resume = cp.Resume
		existing.UpdatedAt = cp.UpdatedAt
	}
	s.mu.Unlock()

	s.bumpListVersion(cp.WorkspaceID)
	logger.Debugf(ctx, "[loopcfg] replaced: jobId=%s nodes=%d totalSteps=%d", jobID, model.CountFlowNodes(cfg.Flow), cp.Progress.TotalSteps)
	return cp.Progress, nil
}

// reconcileProgressToFlow recomputes the progress denominator and drops any
// resume cursor / iteration result / group-iteration record that no longer
// points at a step that exists in flow. After a structure edit, a stale
// Resume.NextPath could resume into a removed step; a stale CurrentPath could
// mislocate the progress UI; stale Results would inflate the completed/failed
// counters. Clearing Resume lets resumeForContinue recompute a fresh cursor
// from CurrentPath on the next Continue.
func reconcileProgressToFlow(job *model.Job, flow []model.FlowNode) {
	if job.Progress == nil {
		job.Progress = &model.JobProgress{}
	}
	job.Progress.TotalSteps = model.CalcTotalSteps(flow)

	// Enumerate the new flow's valid step paths once, then validate every
	// bookkeeping path against the set (O(depth) per check) instead of
	// re-enumerating the whole tree per path — a long Results list against a
	// deeply-nested flow would otherwise be O(R·N).
	valid := model.BuildStepPathSet(flow)

	if job.Resume != nil && !valid.Contains(job.Resume.NextPath) {
		job.Resume = nil
	}
	if !valid.Contains(job.Progress.CurrentPath) {
		job.Progress.CurrentPath = nil
	}

	if len(job.Progress.Results) > 0 {
		kept := job.Progress.Results[:0]
		for _, r := range job.Progress.Results {
			if valid.Contains(r.Path) {
				kept = append(kept, r)
			}
		}
		job.Progress.Results = kept
		job.Progress.CompletedCount, job.Progress.FailedCount = countIterationResults(kept)
	}

	// GroupActualIterations is a display-only early-stop aid keyed by group
	// path; a structure edit invalidates those keys. It is rebuilt at runtime
	// when a group next breaks early, so dropping it here is safe.
	job.Progress.GroupActualIterations = nil
}

// UpdateRunningStepFields applies per-step editable fields onto a running job's
// live flow. See the Service interface for the contract.
func (s *serviceImpl) UpdateRunningStepFields(ctx context.Context, jobID string, newFlow []model.FlowNode) error {
	if len(newFlow) == 0 {
		return fmt.Errorf("loopConfig.flow must not be empty")
	}
	// Validate the editable fields up front (pure, no shared state). The
	// structure is checked separately against the live flow below; here we only
	// guard against illegal field values — an empty prompt / evaluator message,
	// a session-creating step with no agentType, etc. Without this a running
	// edit could write a config that only fails when the step later executes.
	if err := model.ValidateFlow(newFlow, 0); err != nil {
		return err
	}

	// Hold the persist shard across the whole check→save→commit so the sequence
	// is mutually exclusive with Start / Continue / ReplaceLoopConfig.
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	// The handler decides ReplaceLoopConfig vs UpdateRunningStepFields from a
	// status snapshot that may be stale by the time we get here. Re-check under
	// the lock: this path is only valid for a still-running job (it mutates the
	// live flow the runLoop goroutine walks). A job that already finished must
	// not take the running-only path.
	if existing.Status != model.JobStatusRunning {
		s.mu.Unlock()
		return ErrJobNotRunning
	}
	if existing.LoopConfig == nil {
		s.mu.Unlock()
		return ErrNoLoopConfig
	}
	if !flowStructureEqual(existing.LoopConfig.Flow, newFlow) {
		s.mu.Unlock()
		return ErrLoopStructureChanged
	}
	// Save-then-commit: apply onto a deep copy and persist that, so a save
	// failure leaves the live in-memory flow (and the running loop) untouched
	// rather than returning an error while the edit silently took effect.
	cp := existing.DeepCopy()
	applyEditableFields(cp.LoopConfig.Flow, newFlow)
	cp.UpdatedAt = time.Now()
	s.mu.Unlock()

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return err
	}

	// Disk committed. Mirror onto the live flow (the running loop reads it via
	// liveStepFields), gated on the pointer not having been swapped by a
	// concurrent reload — same guard as updateJobField.
	s.mu.Lock()
	if cur, ok := s.jobs[jobID]; ok && cur == existing {
		applyEditableFields(existing.LoopConfig.Flow, newFlow)
		existing.UpdatedAt = cp.UpdatedAt
	}
	s.mu.Unlock()

	s.bumpListVersion(cp.WorkspaceID)
	logger.Debugf(ctx, "[loopcfg] running step fields updated: jobId=%s", jobID)
	return nil
}

// flowStructureEqual reports whether two flow trees have identical structure —
// same node count and order, and matching id / type / round settings / script
// binding / continueOnError / nesting. The per-step editable fields (message,
// agentType, modelId, acpMode) and the cosmetic label are intentionally ignored
// so a running edit that only touches those is accepted; anything else counts
// as a structure change and is rejected (ErrLoopStructureChanged).
func flowStructureEqual(a, b []model.FlowNode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		na, nb := a[i], b[i]
		if na.ID != nb.ID ||
			na.Type != nb.Type ||
			na.RoundMode != nb.RoundMode ||
			na.RoundType != nb.RoundType ||
			na.RepeatCount != nb.RepeatCount ||
			na.IterationCount != nb.IterationCount ||
			na.ScriptID != nb.ScriptID ||
			na.ScriptName != nb.ScriptName ||
			na.ContinueOnError != nb.ContinueOnError {
			return false
		}
		if !flowStructureEqual(na.Children, nb.Children) {
			return false
		}
	}
	return true
}

// applyEditableFields copies the per-step editable fields (and the cosmetic
// label) from src onto dst in lockstep. It assumes flowStructureEqual(dst, src)
// already holds, so the trees have identical shape and can be walked together.
func applyEditableFields(dst, src []model.FlowNode) {
	for i := range dst {
		dst[i].Label = src[i].Label
		if dst[i].Type == model.FlowNodeTypeStep {
			dst[i].Message = src[i].Message
			dst[i].AgentType = src[i].AgentType
			dst[i].StepModelID = src[i].StepModelID
			dst[i].ACPMode = src[i].ACPMode
		}
		applyEditableFields(dst[i].Children, src[i].Children)
	}
}
