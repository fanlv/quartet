package schedule

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/types/model"
)

// ErrScheduleMaxConcurrent is returned by RunNow when a manual trigger is
// refused because the schedule already has maxConcurrent runs in flight. The
// HTTP layer maps it to 409 Conflict and, unlike a real trigger failure, it is
// NOT recorded on the task (no slot was taken, no run was attempted).
var ErrScheduleMaxConcurrent = errors.New("schedule max concurrent reached")

// TriggerFunc is called by the scheduler to create and start a job for the given task.
type TriggerFunc func(ctx context.Context, task *model.ScheduledTask) (jobID string, err error)

// Scheduler manages cron-based execution of scheduled tasks.
type Scheduler struct {
	svc     Service
	trigger TriggerFunc

	mu           sync.Mutex
	runningCount map[string]int // scheduleID -> number of currently executing jobs
	taskLocks    sync.Map       // scheduleID -> *sync.Mutex (protects read-modify-write on task files)
	stopCh       chan struct{}
	done         chan struct{}
}

func NewScheduler(svc Service, trigger TriggerFunc) *Scheduler {
	return &Scheduler{
		svc:          svc,
		trigger:      trigger,
		runningCount: make(map[string]int),
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Start begins the scheduling loop. It checks every minute for tasks that should fire.
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	close(s.stopCh)
	<-s.done
}

// Reload is a no-op signal for future use. The tick-based scheduler
// re-reads all tasks every minute, so changes take effect automatically.
func (s *Scheduler) Reload() {
	// no-op: tasks are read from service on each tick
}

func (s *Scheduler) loop(ctx context.Context) {
	defer close(s.done)

	// Align to the start of the next minute for consistent firing.
	now := time.Now()
	nextMinute := now.Truncate(time.Minute).Add(time.Minute)
	alignTimer := time.NewTimer(time.Until(nextMinute))

	select {
	case <-alignTimer.C:
	case <-s.stopCh:
		alignTimer.Stop()
		return
	case <-ctx.Done():
		alignTimer.Stop()
		return
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// Fire immediately for the aligned minute.
	s.tick(ctx)

	for {
		select {
		case <-ticker.C:
			s.tick(ctx)
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	tasks, err := s.svc.List(ctx)
	if err != nil {
		logger.Errorf(ctx, "[scheduler] list tasks failed: %v", err)
		return
	}

	now := time.Now()
	for _, task := range tasks {
		if !task.Enabled {
			continue
		}
		if !cronMatches(task.CronExpr, now) {
			continue
		}
		s.tryTrigger(ctx, task)
	}
}

// tryTrigger attempts to trigger a scheduled task, respecting maxConcurrent.
// runningCount is incremented here and decremented in MarkDone when the job finishes.
func (s *Scheduler) tryTrigger(ctx context.Context, task *model.ScheduledTask) {
	s.mu.Lock()
	maxConcurrent := task.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	running := s.runningCount[task.ID]
	if running >= maxConcurrent {
		s.mu.Unlock()
		logger.Debugf(ctx, "[scheduler] skip %s (%s): %d/%d running", task.ID, task.Name, running, maxConcurrent)
		return
	}
	s.runningCount[task.ID]++
	s.mu.Unlock()

	// Capture immutable values for the goroutine to avoid data races on *task.
	scheduleID := task.ID
	scheduleName := task.Name
	cronExpr := task.CronExpr

	safe.Go(ctx, func() {
		// jobCreated flips true once trigger returns a job that will later call
		// MarkDone (which releases the running slot). Until then, a panic must
		// release the slot here — otherwise safe.Go recovers the panic but the
		// runningCount stays incremented forever, eventually wedging this
		// schedule at maxConcurrent.
		jobCreated := false
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf(ctx, "[scheduler] trigger panic: schedule=%s (%s) err=%v", scheduleID, scheduleName, r)
				if !jobCreated {
					s.decrRunning(scheduleID)
					s.RecordTrigger(context.Background(), scheduleID, "", cronExpr, fmt.Errorf("panic: %v", r))
				}
			}
		}()

		logger.Debugf(ctx, "[scheduler] trigger: schedule=%s (%s) cron=%q", scheduleID, scheduleName, cronExpr)
		jobID, err := s.trigger(ctx, task)
		if err != nil {
			logger.Errorf(ctx, "[scheduler] trigger failed: schedule=%s (%s) err=%v", scheduleID, scheduleName, err)
			s.decrRunning(scheduleID)
			// Pass the real jobID (empty for stage-one failures, set once a run
			// job was created — e.g. a graph trigger that failed to start). When
			// non-empty, RecordTrigger points LastRunJobID at that real, openable
			// run record so the failure isn't a dangling reference.
			s.RecordTrigger(context.Background(), scheduleID, jobID, cronExpr, err)
			return
		}
		jobCreated = true
		logger.Debugf(ctx, "[scheduler] triggered: schedule=%s (%s) job=%s", scheduleID, scheduleName, jobID)
		s.RecordTrigger(context.Background(), scheduleID, jobID, cronExpr, nil)
	})
}

// RunNow manually triggers a task, respecting maxConcurrent.
// Returns the job ID or an error.
//
// RunNow records the trigger outcome itself (success or failure) via
// RecordTrigger, so cron (tryTrigger) and manual paths share one failure-record
// rule per the design's "手动立即运行与 cron 触发统一失败记录". An over-limit
// refusal returns ErrScheduleMaxConcurrent and is intentionally NOT recorded: no
// slot was taken and no run was attempted. RecordTrigger runs on a background
// context so a disconnected HTTP request can't cancel the status write-back.
func (s *Scheduler) RunNow(ctx context.Context, task *model.ScheduledTask) (string, error) {
	s.mu.Lock()
	maxConcurrent := task.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	if s.runningCount[task.ID] >= maxConcurrent {
		current := s.runningCount[task.ID]
		s.mu.Unlock()
		return "", fmt.Errorf("%w (%d/%d), please wait for current run to finish", ErrScheduleMaxConcurrent, current, maxConcurrent)
	}
	s.runningCount[task.ID]++
	s.mu.Unlock()

	jobID, err := s.trigger(ctx, task)
	if err != nil {
		// Trigger failed — no job will call MarkDone, so decrement now.
		s.decrRunning(task.ID)
		s.RecordTrigger(context.Background(), task.ID, jobID, task.CronExpr, err)
		return jobID, err
	}
	// runningCount will be decremented in MarkDone when the job finishes.
	s.RecordTrigger(context.Background(), task.ID, jobID, task.CronExpr, nil)
	return jobID, nil
}

// MarkDone updates a task's LastStatus when its job completes
// and decrements the running count so the scheduler can trigger new runs.
//
// jobID is the ID of the job that actually finished. It is NOT read from
// task.LastRunJobID: with maxConcurrent>1 (or a fast-completing job whose
// RecordTrigger hasn't landed yet) LastRunJobID can point at a sibling
// run, and using it here would make logs and status reflect the wrong job.
func (s *Scheduler) MarkDone(ctx context.Context, scheduleID, jobID string, status model.JobStatus) {
	s.decrRunning(scheduleID)

	lock := s.getTaskLock(scheduleID)
	lock.Lock()
	defer lock.Unlock()

	task, err := s.svc.Get(ctx, scheduleID)
	if err != nil {
		logger.Errorf(ctx, "[scheduler] MarkDone load failed: schedule=%s job=%s status=%s err=%v", scheduleID, jobID, status, err)
		return
	}
	if task == nil {
		logger.Warnf(ctx, "[scheduler] MarkDone skipped: schedule=%s job=%s status=%s task not found", scheduleID, jobID, status)
		return
	}
	// Only write LastStatus when the finishing job is the latest one tracked
	// on the task. With maxConcurrent>1, older runs finishing out of order
	// would otherwise overwrite the newer run's status.
	if task.LastRunJobID == "" || task.LastRunJobID == jobID {
		task.LastStatus = status
	} else {
		logger.Debugf(ctx, "[scheduler] MarkDone status skipped (concurrent run): schedule=%s done_job=%s latest_job=%s status=%s", scheduleID, jobID, task.LastRunJobID, status)
	}
	task.UpdatedAt = time.Now()
	if err := s.svc.Save(ctx, task); err != nil {
		logger.Errorf(ctx, "[scheduler] MarkDone save failed: schedule=%s job=%s err=%v", scheduleID, jobID, err)
		return
	}
	if status == model.JobStatusFailed {
		logger.Infof(ctx, "[scheduler] done (failed): schedule=%s (%s) job=%s status=%s", scheduleID, task.Name, jobID, status)
	} else {
		logger.Debugf(ctx, "[scheduler] done: schedule=%s (%s) job=%s status=%s", scheduleID, task.Name, jobID, status)
	}
}

// decrRunning safely decrements the running count for a schedule. Emits a
// warn if the counter underflows — that indicates MarkDone was called more
// times than tryTrigger, which historically signalled a duplicate-done bug
// (e.g. a second backend process sharing the same task store). The counter
// is clamped to 0 so subsequent triggers still work.
func (s *Scheduler) decrRunning(scheduleID string) {
	s.mu.Lock()
	s.runningCount[scheduleID]--
	if s.runningCount[scheduleID] < 0 {
		logger.Warnf(context.Background(), "[scheduler] decrRunning underflow: schedule=%s count=%d (likely duplicate MarkDone)", scheduleID, s.runningCount[scheduleID])
	}
	if s.runningCount[scheduleID] <= 0 {
		delete(s.runningCount, scheduleID)
	}
	s.mu.Unlock()
}

// getTaskLock returns the per-schedule mutex for serializing read-modify-write operations on task files.
func (s *Scheduler) getTaskLock(scheduleID string) *sync.Mutex {
	v, _ := s.taskLocks.LoadOrStore(scheduleID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// RecordTrigger atomically updates a scheduled task's status after a trigger attempt.
// Called by both tryTrigger (cron) and RunNow (manual) paths to avoid duplicated logic.
func (s *Scheduler) RecordTrigger(ctx context.Context, scheduleID, jobID, cronExpr string, triggerErr error) {
	lock := s.getTaskLock(scheduleID)
	lock.Lock()
	defer lock.Unlock()

	task, err := s.svc.Get(ctx, scheduleID)
	if err != nil || task == nil {
		logger.Errorf(ctx, "[scheduler] RecordTrigger: load task failed: schedule=%s err=%v", scheduleID, err)
		return
	}

	now := time.Now()
	task.LastRunAt = &now
	task.RunCount++
	if triggerErr != nil {
		task.LastStatus = model.JobStatusFailed
		task.LastTriggerError = triggerErr.Error()
		// Point LastRunJobID at the run record only when one was actually
		// created (stage-two graph failure: job exists but StartRun failed).
		// Stage-one failures pass an empty jobID — keep the previous reference
		// untouched so the schedule doesn't claim a run that never existed.
		if jobID != "" {
			task.LastRunJobID = jobID
		}
	} else {
		// Preserve a terminal LastStatus only when it belongs to the *same*
		// jobID we're about to record — that's the race we care about (a
		// fast-completing job whose MarkDone wrote the terminal status before
		// RecordTrigger landed). For a different jobID, the existing terminal
		// status is from the previous run and must be cleared, otherwise the
		// schedule shows e.g. "completed" while the new job is still running.
		sameJobAlreadyDone := task.LastRunJobID == jobID && isTerminalStatus(task.LastStatus)
		task.LastRunJobID = jobID
		if sameJobAlreadyDone {
			logger.Debugf(ctx, "[scheduler] RecordTrigger: preserving terminal status (MarkDone won race): schedule=%s job=%s status=%s", scheduleID, jobID, task.LastStatus)
		} else {
			task.LastStatus = model.JobStatusRunning
		}
		task.LastTriggerError = ""
	}
	task.NextRunAt = NextCronTime(cronExpr, now)
	task.UpdatedAt = time.Now()

	if saveErr := s.svc.Save(ctx, task); saveErr != nil {
		logger.Errorf(ctx, "[scheduler] RecordTrigger: save failed: schedule=%s err=%v", scheduleID, saveErr)
	}
}

// isTerminalStatus returns true if the status indicates a job has finished.
func isTerminalStatus(s model.JobStatus) bool {
	return s == model.JobStatusCompleted || s == model.JobStatusFailed || s == model.JobStatusStopped
}

// ---- Cron expression parser (5-field: min hour dom month dow) ----

func cronMatches(expr string, t time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	return fieldMatches(fields[0], t.Minute(), 0, 59) &&
		fieldMatches(fields[1], t.Hour(), 0, 23) &&
		fieldMatches(fields[2], t.Day(), 1, 31) &&
		fieldMatches(fields[3], int(t.Month()), 1, 12) &&
		fieldMatches(fields[4], int(t.Weekday()), 0, 6)
}

func fieldMatches(field string, value, min, max int) bool {
	for _, part := range strings.Split(field, ",") {
		if matchPart(part, value, min, max) {
			return true
		}
	}
	return false
}

func matchPart(part string, value, min, max int) bool {
	// Handle step: */5, 1-10/2, etc.
	step := 1
	if idx := strings.Index(part, "/"); idx >= 0 {
		s, err := strconv.Atoi(part[idx+1:])
		if err != nil || s <= 0 {
			return false
		}
		step = s
		part = part[:idx]
	}

	// Handle range: 1-5
	if idx := strings.Index(part, "-"); idx >= 0 {
		lo, err1 := strconv.Atoi(part[:idx])
		hi, err2 := strconv.Atoi(part[idx+1:])
		if err1 != nil || err2 != nil {
			return false
		}
		if value < lo || value > hi {
			return false
		}
		return (value-lo)%step == 0
	}

	// Wildcard
	if part == "*" {
		return (value-min)%step == 0
	}

	// Exact value
	v, err := strconv.Atoi(part)
	if err != nil {
		return false
	}
	return v == value
}

// NextCronTime calculates the next fire time after `after` for the given cron expression.
func NextCronTime(expr string, after time.Time) *time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	// Search up to 366 days ahead.
	limit := t.Add(366 * 24 * time.Hour)
	for t.Before(limit) {
		if cronMatches(expr, t) {
			return &t
		}
		t = t.Add(time.Minute)
	}
	return nil
}

// ValidateCronExpr checks if a cron expression is syntactically valid.
func ValidateCronExpr(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("cron expression must have exactly 5 fields, got %d", len(fields))
	}
	names := []string{"minute", "hour", "day-of-month", "month", "day-of-week"}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	for i, field := range fields {
		if err := validateCronField(field, names[i], ranges[i][0], ranges[i][1]); err != nil {
			return err
		}
	}
	return nil
}

func validateCronField(field, name string, min, max int) error {
	for _, part := range strings.Split(field, ",") {
		raw := part
		// Strip step suffix
		if idx := strings.Index(part, "/"); idx >= 0 {
			s, err := strconv.Atoi(part[idx+1:])
			if err != nil || s <= 0 {
				return fmt.Errorf("invalid step in %s field: %s", name, raw)
			}
			part = part[:idx]
		}
		if part == "*" {
			continue
		}
		if idx := strings.Index(part, "-"); idx >= 0 {
			lo, err1 := strconv.Atoi(part[:idx])
			hi, err2 := strconv.Atoi(part[idx+1:])
			if err1 != nil || err2 != nil || lo < min || hi > max || lo > hi {
				return fmt.Errorf("invalid range in %s field: %s", name, raw)
			}
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v < min || v > max {
			return fmt.Errorf("invalid value in %s field: %s (must be %d-%d)", name, raw, min, max)
		}
	}
	return nil
}
