package job

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/strutil"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

// substituteVars performs a single-pass replacement of {{key}} placeholders
// using strings.NewReplacer. This avoids nondeterminism from Go map iteration
// order: with a naive for-range + ReplaceAll loop, if variable A's value
// contains "{{B}}", the result depends on whether A or B is iterated first.
// NewReplacer scans left-to-right once, so replacement output is never re-matched.
func (s *serviceImpl) substituteVars(text string, job *model.Job) string {
	s.mu.RLock()
	vars := job.LoopConfig.Variables
	disabled := job.LoopConfig.DisabledVars
	if len(vars) == 0 && len(disabled) == 0 {
		s.mu.RUnlock()
		return text
	}
	disabledSet := make(map[string]struct{}, len(disabled))
	for _, k := range disabled {
		disabledSet[k] = struct{}{}
	}
	oldnew := make([]string, 0, (len(vars)+len(disabled))*2)
	for k, v := range vars {
		if _, off := disabledSet[k]; off {
			// Disabled user variable: render to empty string but keep its
			// stored value untouched so re-enabling restores it.
			oldnew = append(oldnew, "{{"+k+"}}", "")
			continue
		}
		oldnew = append(oldnew, "{{"+k+"}}", v)
	}
	// A disabled key without an entry in Variables (e.g. an empty-value var
	// toggled off) still blanks its placeholder.
	for _, k := range disabled {
		if _, ok := vars[k]; !ok {
			oldnew = append(oldnew, "{{"+k+"}}", "")
		}
	}
	s.mu.RUnlock()
	return strings.NewReplacer(oldnew...).Replace(text)
}

func upsertLoopVariables(job *model.Job, vars map[string]string) {
	if job.LoopConfig == nil || len(vars) == 0 {
		return
	}
	if job.LoopConfig.Variables == nil {
		job.LoopConfig.Variables = make(map[string]string)
	}
	for k, v := range vars {
		job.LoopConfig.Variables[k] = v
	}
}

func (s *serviceImpl) injectBuiltinVars(ctx context.Context, job *model.Job) {
	if job.LoopConfig == nil {
		return
	}
	s.mu.Lock()
	logger.Debugf(ctx, "[step] injectBuiltinVars: jobId=%s title=%s", job.ID, job.Title)
	upsertLoopVariables(job, map[string]string{
		consts.VarJobID:       job.ID,
		consts.VarJobTitle:    job.Title,
		consts.VarJobWorkdir:  job.Workdir,
		consts.VarWorkspaceID: job.WorkspaceID,
	})
	s.mu.Unlock()
}

// injectPerRoundVars injects dynamic per-round builtin variables:
//   - _current_time: current timestamp (RFC3339)
//   - _current_path: the step path as a string (e.g. "0.1.2.0")
//   - _last_assistant_msg: the Content from the last completed iteration result
func (s *serviceImpl) injectPerRoundVars(ctx context.Context, job *model.Job, path []int) {
	if job.LoopConfig == nil {
		return
	}

	now := time.Now().Format(time.RFC3339)
	pathStr := model.StepPathKey(path)

	var lastAssistant string
	s.mu.Lock()
	if job.Progress != nil && len(job.Progress.Results) > 0 {
		lastAssistant = job.Progress.Results[len(job.Progress.Results)-1].Content
	}
	upsertLoopVariables(job, map[string]string{
		consts.VarCurrentTime:      now,
		consts.VarCurrentPath:      pathStr,
		consts.VarLastAssistantMsg: lastAssistant,
	})
	s.mu.Unlock()

	logger.Debugf(ctx, "[step] injectPerRoundVars: jobId=%s path=%s time=%s lastAssistant=%s",
		job.ID, pathStr, now, strutil.TruncateRunesWithEllipsis(lastAssistant, 100))
}

// applyVarsToJob merges extracted variables into job.LoopConfig.Variables
// and persists the job state. Variable extraction is best-effort (the loop
// can keep running with the live in-memory map even if disk write fails),
// so a persist failure is annotated via recordPersistWarning rather than
// surfaced — same pattern as updateResume and initAndAttachSession.
func (s *serviceImpl) applyVarsToJob(ctx context.Context, job *model.Job, vars map[string]string, source string) {
	if len(vars) == 0 {
		return
	}
	s.mu.Lock()
	if job.LoopConfig != nil {
		upsertLoopVariables(job, vars)
	}
	s.mu.Unlock()
	if err := s.saveJobWithRetry(ctx, job, source); err != nil {
		s.recordPersistWarning(ctx, job, source, err)
	}
	logger.Debugf(ctx, "[step] extracted %d vars: source=%s jobId=%s", len(vars), source, job.ID)
}

// extractSetVars parses the agent response for <<SET_VAR:key=value>> patterns
// and returns the extracted key-value pairs.
var setVarPattern = regexp.MustCompile(`<<SET_VAR:(\w+)=(.+?)>>`)

func extractSetVars(content string) map[string]string {
	matches := setVarPattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	vars := make(map[string]string, len(matches))
	for _, m := range matches {
		vars[m[1]] = m[2]
	}
	return vars
}
