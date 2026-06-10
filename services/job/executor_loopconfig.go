package job

import (
	"context"
	"fmt"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/consts"
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
		return nil, fmt.Errorf("%w: loopConfig.flow must not be empty", ErrLoopConfigInvalid)
	}
	if err := model.ValidateFlow(cfg.Flow, 0); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoopConfigInvalid, err)
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
	var oldFlow []model.FlowNode
	if cp.LoopConfig != nil {
		oldFlow = cp.LoopConfig.Flow
	}
	oldStepNodeIDs := model.BuildStepPathNodeIDMap(oldFlow)
	newFlow := model.DeepCopyFlowNodes(cfg.Flow)
	// A pure field/label edit (same node tree) must NOT recompute the
	// denominator or drop the early-stop display maps: a completed loop that
	// broke early backfilled TotalSteps down to what actually ran, and
	// CalcTotalSteps would inflate it back to the static cap — regressing a
	// 100% bar. Only a real structure change invalidates that bookkeeping.
	structureChanged := !flowStructureEqual(oldFlow, newFlow)

	if cp.LoopConfig == nil {
		cp.LoopConfig = &model.LoopConfig{}
	}
	cp.LoopConfig.Flow = newFlow
	// Flow is now the canonical form — drop any legacy flat fields so a later
	// MigrateLoopConfig can't resurrect a stale Rounds list over the new tree.
	cp.LoopConfig.Rounds = nil
	cp.LoopConfig.IterationCount = 0
	// Variables: only overwrite when the caller supplied a map. The editor does
	// not manage runtime-injected builtins (_current_time, jobId, …); leaving
	// the existing map intact preserves user-defined template vars, and Start /
	// Continue re-inject the builtins at runLoop entry regardless.
	if cfg.Variables != nil {
		cp.LoopConfig.Variables = copyStringMap(cfg.Variables)
	}
	reconcileProgressToFlow(cp, newFlow, oldStepNodeIDs, structureChanged)
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
	logger.Debugf(ctx, "[loopcfg] replaced: jobId=%s nodes=%d totalSteps=%d", jobID, model.CountFlowNodes(newFlow), cp.Progress.TotalSteps)
	return cp.Progress, nil
}

// reconcileProgressToFlow reconciles a job's progress bookkeeping against an
// edited flow. When structureChanged is false the node tree is identical (only
// per-step fields / labels moved), so every recorded path still resolves and
// the denominator / early-stop display maps must be preserved as-is — a
// completed loop that broke early backfilled TotalSteps below the static cap,
// and recomputing it would regress a full progress bar. When structureChanged
// is true a stale Resume.NextPath could resume into a removed step, a stale
// CurrentPath could mislocate the UI, and stale Results would inflate the
// completed/failed counters, so each is dropped if it no longer points at a
// step that survives in the new flow. To avoid Continue silently restarting
// from the first step (resumeForContinue derives the next path from
// CurrentPath, and a nil CurrentPath with a non-zero completed count maps to
// "before the first step"), a cleared Resume is re-anchored onto the first
// step that has no surviving successful result — the genuine resume point.
func reconcileProgressToFlow(job *model.Job, flow []model.FlowNode, oldStepNodeIDs map[string]string, structureChanged bool) {
	if job.Progress == nil {
		job.Progress = &model.JobProgress{}
	}
	if !structureChanged {
		// Identical structure: paths, counts, denominator and early-stop
		// display maps all stay valid. Nothing to reconcile.
		return
	}
	job.Progress.TotalSteps = model.CalcTotalSteps(flow)

	// Enumerate the new flow's valid step paths once, then validate every
	// bookkeeping path against the set (O(depth) per check) instead of
	// re-enumerating the whole tree per path — a long Results list against a
	// deeply-nested flow would otherwise be O(R·N).
	valid := model.BuildStepPathSet(flow)
	newStepNodeIDs := model.BuildStepPathNodeIDMap(flow)

	if job.Resume != nil && !sameStepIdentity(valid, oldStepNodeIDs, newStepNodeIDs, job.Resume.NextPath) {
		job.Resume = nil
	}
	if !sameStepIdentity(valid, oldStepNodeIDs, newStepNodeIDs, job.Progress.CurrentPath) {
		job.Progress.CurrentPath = nil
	}

	if len(job.Progress.Results) > 0 {
		kept := job.Progress.Results[:0]
		for _, r := range job.Progress.Results {
			if sameStepIdentity(valid, oldStepNodeIDs, newStepNodeIDs, r.Path) {
				kept = append(kept, r)
			}
		}
		job.Progress.Results = kept
		job.Progress.CompletedCount, job.Progress.FailedCount = countIterationResults(kept)
	} else {
		job.Progress.CompletedCount, job.Progress.FailedCount = 0, 0
	}

	// Re-anchor a cleared Resume onto the first step that still has no
	// surviving result. Without this, a nil Resume plus a non-zero completed
	// count makes resumeForContinue compute NextStepPath(flow, nil) → the
	// first step, re-running every step that already completed. Resuming at
	// the first incomplete step re-runs only what the edit invalidated
	// (removed/replaced nodes whose results were dropped) and what never ran,
	// never a step that still owns a valid result. A surviving valid Resume is
	// authoritative and left untouched; a zero completed count needs no anchor
	// (resumeForContinue starts fresh).
	if job.Resume == nil && job.Progress.CompletedCount > 0 {
		if next := firstIncompleteStepPath(flow, job.Progress.Results); next != nil {
			job.Resume = &model.JobResume{NextPath: next}
		}
	}

	// LastError must track the surviving failed results. If the edit removed the
	// step that failed, FailedCount drops but a stale LastError would still be
	// shown by the UI (LoopProgress keys on status==='failed'). Rebuild it from
	// the last surviving failed result, or clear it when none remain.
	job.Progress.LastError = ""
	for i := len(job.Progress.Results) - 1; i >= 0; i-- {
		if r := job.Progress.Results[i]; !r.Success && r.Error != "" {
			job.Progress.LastError = r.Error
			break
		}
	}

	// GroupActualIterations is a display-only early-stop aid keyed by group
	// path; a structure edit invalidates those keys. It is rebuilt at runtime
	// when a group next breaks early, so dropping it here is safe.
	job.Progress.GroupActualIterations = nil
	job.Progress.GroupActualLeafCounts = nil
}

func sameStepIdentity(valid model.StepPathSet, oldStepNodeIDs, newStepNodeIDs map[string]string, path []int) bool {
	if len(path) == 0 {
		return true
	}
	if !valid.Contains(path) {
		return false
	}
	key := model.StepPathKey(path)
	oldID, oldOK := oldStepNodeIDs[key]
	newID, newOK := newStepNodeIDs[key]
	return oldOK && newOK && oldID == newID
}

// firstIncompleteStepPath walks flow in execution order and returns the path of
// the first leaf step that has no successful result in results. Used to
// re-anchor resume after a structure edit drops the old cursor: resuming here
// re-runs only steps that never finished (or whose result was invalidated by
// the edit), not steps that still own a valid successful result. Returns nil
// when every step already has a successful result (nothing left to resume).
func firstIncompleteStepPath(flow []model.FlowNode, results []model.IterationResult) []int {
	succeeded := make(map[string]struct{}, len(results))
	for _, r := range results {
		if r.Success {
			succeeded[model.StepPathKey(r.Path)] = struct{}{}
		}
	}
	for path := model.NextStepPath(flow, nil); path != nil; path = model.NextStepPath(flow, path) {
		if _, ok := succeeded[model.StepPathKey(path)]; !ok {
			return path
		}
	}
	return nil
}

func copyStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func isBuiltinLoopVar(k string) bool {
	switch k {
	case consts.VarJobID,
		consts.VarJobTitle,
		consts.VarJobWorkdir,
		consts.VarWorkspaceID,
		consts.VarCurrentTime,
		consts.VarCurrentPath,
		consts.VarLastAssistantMsg:
		return true
	default:
		return false
	}
}

// stringMapsEqual compares two variable maps for user-defined equality. Runtime
// builtins (_job_id, _current_time, …) are injected into the live map during
// execution but never sent by the client, so only known builtin keys are ignored
// on both sides — unknown "_" keys may be user/template variables and must still
// be compared rather than silently accepted and dropped.
func stringMapsEqual(a, b map[string]string) bool {
	count := func(m map[string]string) int {
		n := 0
		for k := range m {
			if !isBuiltinLoopVar(k) {
				n++
			}
		}
		return n
	}
	if count(a) != count(b) {
		return false
	}
	for k, va := range a {
		if isBuiltinLoopVar(k) {
			continue
		}
		if vb, ok := b[k]; !ok || vb != va {
			return false
		}
	}
	return true
}

// UpdateRunningStepFields applies per-step editable fields onto a running job's
// live flow. See the Service interface for the contract.
func (s *serviceImpl) UpdateRunningStepFields(ctx context.Context, jobID string, cfg *model.LoopConfig) error {
	if cfg == nil {
		return ErrNoLoopConfig
	}
	// Normalize here rather than relying on the HTTP handler to pre-migrate:
	// the service layer must not depend on the caller having folded a legacy
	// LoopConfig into the Flow tree first. Mirrors ReplaceLoopConfig's own
	// migrate-then-validate flow.
	model.MigrateLoopConfig(cfg)
	newFlow := cfg.Flow
	if len(newFlow) == 0 {
		return fmt.Errorf("%w: loopConfig.flow must not be empty", ErrLoopConfigInvalid)
	}
	// Validate the editable fields up front (pure, no shared state). The
	// structure is checked separately against the live flow below; here we only
	// guard against illegal field values — an empty prompt / evaluator message,
	// a session-creating step with no agentType, etc. Without this a running
	// edit could write a config that only fails when the step later executes.
	if err := model.ValidateFlow(newFlow, 0); err != nil {
		return fmt.Errorf("%w: %v", ErrLoopConfigInvalid, err)
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
	// Variables are substituted live during execution; a running job cannot
	// re-apply them safely, so reject (rather than silently drop) any change.
	// A nil cfg.Variables means "caller did not touch variables" — the client
	// may omit the map when only editing step fields. A non-nil map that differs
	// from the live set is a real edit attempt and must be refused with a clear
	// error instead of returning success and ignoring it.
	if cfg.Variables != nil && !stringMapsEqual(existing.LoopConfig.Variables, cfg.Variables) {
		s.mu.Unlock()
		return ErrLoopVariablesChanged
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

// canonicalRoundType maps the empty RoundType to its semantic default
// (prompt), matching how ValidateFlow and the executor treat "" everywhere
// else. Used by flowStructureEqual so a legacy node with RoundType="" and a
// client that canonicalized it to "prompt" are not seen as a structure change.
func canonicalRoundType(rt model.RoundType) model.RoundType {
	if rt == "" {
		return model.RoundTypePrompt
	}
	return rt
}

// canonicalRoundMode maps the empty RoundMode to its semantic default (none),
// matching roundModeForStepPath.
func canonicalRoundMode(rm model.RoundMode) model.RoundMode {
	if rm == "" {
		return model.RoundModeNone
	}
	return rm
}

// canonicalCount maps any count < 1 to 1, matching the total-step and execution
// math that treats 0 repeat/iteration counts as a single pass.
func canonicalCount(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// flowStructureEqual reports whether two flow trees have identical structure —
// same node count and order, and matching id / type / round settings / script
// binding / nesting. The per-step editable fields (message, agentType, modelId,
// acpMode) and the cosmetic label are intentionally ignored so a running edit
// that only touches those is accepted; anything else counts as a structure
// change and is rejected (ErrLoopStructureChanged).
//
// Round settings are compared in canonical form: an empty RoundType/RoundMode
// and a count < 1 are normalized to their semantic defaults (prompt / none / 1)
// before comparison. Otherwise a legacy node with empty defaults and a client
// that round-tripped it through canonicalization would be misread as a
// structure change — wrongly rejecting a running field edit with
// ErrLoopStructureChanged, or forcing ReplaceLoopConfig to recompute progress
// and drop early-stop backfill display data on a pure field edit.
func flowStructureEqual(a, b []model.FlowNode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		na, nb := a[i], b[i]
		if na.ID != nb.ID ||
			na.Type != nb.Type ||
			canonicalRoundMode(na.RoundMode) != canonicalRoundMode(nb.RoundMode) ||
			canonicalRoundType(na.RoundType) != canonicalRoundType(nb.RoundType) ||
			canonicalCount(na.RepeatCount) != canonicalCount(nb.RepeatCount) ||
			canonicalCount(na.IterationCount) != canonicalCount(nb.IterationCount) ||
			na.ScriptID != nb.ScriptID ||
			na.ScriptName != nb.ScriptName {
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
