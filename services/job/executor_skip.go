package job

import (
	"context"
	"regexp"
	"strings"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// Empty-prompt skip (docs/feature/feature-2026-06-11-loop-skip-empty-prompt-step.md):
// a prompt step whose message renders to an empty string after variable
// substitution has nothing to do this round. It is skipped BEFORE a session is
// created: no agent process, no round events, no IterationResult — the only
// execution-side trace is an INFO log. The bookkeeping correction (TotalSteps
// deduction + SkippedPaths entry + resume/current-path advance) is persisted in
// one atomic save so a crash cannot leave the denominator and the skip set
// disagreeing.

// isSkippablePromptStep reports whether the step participates in empty-prompt
// skipping: prompt-type leaf steps only. Evaluator steps must always run (they
// own the loop's stop decision) and shell steps are the variable producers.
func isSkippablePromptStep(node model.FlowNode) bool {
	return node.Type == model.FlowNodeTypeStep &&
		(node.RoundType == "" || node.RoundType == model.RoundTypePrompt)
}

// soleUnresolvedPlaceholder matches a rendered prompt that consists of exactly
// one {{variable}} placeholder and nothing else. Placeholders only survive
// substitution when the variable is undefined, so a match means "the template
// is a single variable that nobody has set". The name charset mirrors the
// shell-side variable extraction (quartet_set / SET_VAR, \w+).
var soleUnresolvedPlaceholder = regexp.MustCompile(`^\{\{\w+\}\}$`)

// renderedPromptEmpty implements the §2.1 skip judgement on a rendered prompt:
//
//  1. blank after trimming — the variables it was built from are all empty;
//  2. exactly one unresolved {{variable}} placeholder — the template is a
//     single variable that is still undefined (typical first round: the shell
//     step that defines it hasn't run yet). Sending the literal placeholder to
//     the model would be the same wasted round as sending nothing.
//
// Mixed templates (fixed text + placeholder) and multi-placeholder templates
// never match: fixed text means the user wants something sent, and a
// multi-variable template can't distinguish "all idle" from "one of them is a
// typo" — neither is silently swallowed. Returns the matched rule for the skip
// log, or "" when the prompt should execute normally.
func renderedPromptEmpty(rendered string) string {
	trimmed := strings.TrimSpace(rendered)
	if trimmed == "" {
		return "empty"
	}
	if soleUnresolvedPlaceholder.MatchString(trimmed) {
		return "sole_unresolved_placeholder"
	}
	return ""
}

// isSkippedPath reports whether stepPath is recorded in the persisted skip set.
// Used on Continue / restart to credit pre-resume slots on the backfilled side
// — skip state is never re-derived from current variable values (they may have
// changed since the skip happened).
func (s *serviceImpl) isSkippedPath(job *model.Job, stepPath []int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return job.Progress != nil && job.Progress.SkippedPaths[model.StepPathKey(stepPath)]
}

// snapshotSkippedPaths returns a copy of the persisted skip set for lock-free
// walking (countSkippedLeaves).
func (s *serviceImpl) snapshotSkippedPaths(job *model.Job) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if job.Progress == nil || len(job.Progress.SkippedPaths) == 0 {
		return nil
	}
	return copyBoolMap(job.Progress.SkippedPaths)
}

// countSkippedLeaves statically expands the subtree rooted at basePath and
// counts the leaf slots present in the skip set. Used when a whole group
// iteration sits before the resume point: its slots were consumed in a prior
// run, but the skipped ones must be credited as backfilled (their TotalSteps
// deduction already happened), not as executed.
func countSkippedLeaves(nodes []model.FlowNode, basePath []int, skipped map[string]bool) int {
	if len(skipped) == 0 {
		return 0
	}
	n := 0
	for i, node := range nodes {
		switch node.Type {
		case model.FlowNodeTypeStep:
			rc := node.RepeatCount
			if rc < 1 {
				rc = 1
			}
			for r := 0; r < rc; r++ {
				if skipped[model.StepPathKey(appendPath(basePath, i, r))] {
					n++
				}
			}
		case model.FlowNodeTypeGroup:
			ic := node.IterationCount
			if ic < 1 {
				ic = 1
			}
			for iter := 0; iter < ic; iter++ {
				n += countSkippedLeaves(node.Children, appendPath(basePath, i, iter), skipped)
			}
		}
	}
	return n
}

// skipEmptyPromptStep performs the skip branch for a prompt step whose
// rendered prompt is empty. It runs INSTEAD of session creation + dispatch:
//
//   - four state changes — TotalSteps deduction, SkippedPaths entry, resume
//     advance, current-path advance — are committed in a single save. A partial
//     write (e.g. current path advanced but resume not) would make Continue's
//     fallback (next-of-CurrentPath) silently hop over a real step.
//   - the deduction is idempotent: a slot already in the set (Continue
//     re-entered a skip whose save never landed) only re-advances the pointers.
//   - the session pointer is intentionally untouched: the step never ran, so it
//     must not consume or reset the session the previous step left behind, even
//     when configured as eachRepeat/beforeRound.
//   - CurrentPath moves to the next pending leaf (or clears at the tail) so the
//     UI never points at a leaf the session plan filtered out.
//
// Returns stepCompleted, or stepStopGraceful when a "stop after step" request
// is consumed at this boundary — the same contract as a step that really ran.
// rule is the renderedPromptEmpty match, logged for traceability.
func (s *serviceImpl) skipEmptyPromptStep(ctx context.Context, run *flowExecution, stepPath []int, rule string) stepResult {
	job := run.job
	nextPath := model.NextStepPath(run.flowRoot, stepPath)
	var nextResume *model.JobResume
	if nextPath != nil {
		sessionID := *run.currentSessionID
		if nextStepStartsFreshSession(run.flowRoot, nextPath) {
			// The next step spawns its own session; carrying one would make its
			// resume guard mistake the advance for a mid-run resume.
			sessionID = ""
		}
		nextResume = &model.JobResume{NextPath: model.CopyPath(nextPath), SessionID: sessionID}
	}

	key := model.StepPathKey(stepPath)
	s.mu.Lock()
	if !job.Progress.SkippedPaths[key] {
		if job.Progress.SkippedPaths == nil {
			job.Progress.SkippedPaths = make(map[string]bool)
		}
		job.Progress.SkippedPaths[key] = true
		if job.Progress.TotalSteps > 0 {
			job.Progress.TotalSteps--
		}
	}
	job.Progress.CurrentPath = model.CopyPath(nextPath)
	job.Progress.CurrentStartedAt = 0
	job.Resume = copyResume(nextResume)
	newTotal := job.Progress.TotalSteps
	actualMap := copyIntMap(job.Progress.GroupActualIterations)
	leafMap := copyIntMap(job.Progress.GroupActualLeafCounts)
	skipped := copyBoolMap(job.Progress.SkippedPaths)
	currentPath := model.CopyPath(job.Progress.CurrentPath)
	s.mu.Unlock()

	logger.Infof(ctx, "[loop] step skipped (rendered prompt empty, rule=%s): jobId=%s path=%v totalSteps=%d skipped=%d",
		rule, job.ID, stepPath, newTotal, len(skipped))

	if err := s.saveJobWithRetry(ctx, job, jobPersistActionSkipEmptyPrompt); err != nil {
		// Best-effort like every other progress persist: warn, keep the
		// in-memory state. If the resume advance never lands, Continue re-enters
		// this slot and re-judges — still-empty re-skips idempotently, a changed
		// variable runs the step normally.
		s.recordPersistWarning(ctx, job, jobPersistActionSkipEmptyPrompt, err)
	}

	s.publishProgressTotalUpdated(job.ID, newTotal, actualMap, leafMap, skipped, currentPath)

	// A "stop after step" request is consumed at a skipped step's boundary
	// exactly like at an executed step's.
	return s.handlePostStepBoundary(ctx, job, stepPath, postStepResume{nextPath: nextPath})
}

func copyBoolMap(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
