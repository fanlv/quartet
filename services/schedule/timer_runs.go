package schedule

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

func (m *TimerManager) Run(ctx context.Context, id, submission string, permitted func(string) bool) (model.TimerExecution, error) {
	submission = strings.TrimSpace(submission)
	if submission == "" || len(submission) > 200 {
		return model.TimerExecution{}, fmt.Errorf("%w: clientRequestId must contain 1 to 200 bytes", ErrTimerInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	task, ledger, err := m.load(ctx, id, now)
	if err != nil {
		return model.TimerExecution{}, err
	}
	if task.Kind == "" {
		task.Kind = model.TimerGraph
	}
	if !permitted(task.Kind) {
		return model.TimerExecution{}, fmt.Errorf("%w: %s timer", ErrTimerForbidden, task.Kind)
	}
	key := timerExecutionID(id, "manual", submission)
	for _, entry := range ledger.Entries {
		if entry.ID == key {
			return m.executionRecord(entry), nil
		}
	}
	target, _, err := m.inspect(ctx, task, ledger)
	if err != nil {
		return model.TimerExecution{}, err
	}
	if ledger.PendingReason != "" {
		return model.TimerExecution{}, fmt.Errorf("%w: %s", ErrTimerConflict, ledger.PendingReason)
	}
	if reason := timerEndReason(task, ledger, now); reason != "" {
		return model.TimerExecution{}, fmt.Errorf("%w: %s", ErrTimerConflict, reason)
	}
	if m.active(ledger, "") >= max(1, task.MaxConcurrent) {
		return model.TimerExecution{}, fmt.Errorf("%w: %s (limit %d)", ErrTimerConflict, ErrScheduleMaxConcurrent, max(1, task.MaxConcurrent))
	}
	entry := m.admit(ctx, ledger, task, target, key, "manual", nil, now)
	if err := m.history.Save(ledger); err != nil {
		return model.TimerExecution{}, err
	}
	m.startLocked(ctx, id, entry.ID)
	return m.executionRecord(entry), nil
}

func timerExecutionID(id, trigger, intent string) string {
	return fmt.Sprintf("run-%x", sha256.Sum256([]byte(id+"\x00"+trigger+"\x00"+intent)))
}

func (m *TimerManager) admit(ctx context.Context, ledger *model.TimerLedger, task *model.ScheduledTask, target *model.TimerTarget, id, trigger string, planned *time.Time, now time.Time) model.TimerEntry {
	entry := model.TimerEntry{Target: target, TimerExecution: model.TimerExecution{
		ID: id, ScheduleID: task.ID, Name: task.Name, Kind: task.Kind, WorkflowID: task.GraphWorkflowID, WorkspaceID: task.WorkspaceID,
		Trigger: trigger, ScheduledAt: planned, TriggeredAt: &now, Status: model.TimerAccepted,
	}}
	if target == nil {
		_, _, err := m.resolve(ctx, task)
		if err == nil {
			err = fmt.Errorf("execution dependencies changed during admission")
		}
		entry.Status, entry.Error, entry.FinishedAt = string(model.JobStatusFailed), err.Error(), &now
		snapshot := *task
		if ledger.ApprovedTarget != nil {
			snapshot = ledger.ApprovedTarget.Task
		}
		entry.Configuration = &snapshot
	} else {
		entry.WorkspaceID = target.Task.WorkspaceID
	}
	ledger.Entries = append(ledger.Entries, entry)
	return entry
}

func (m *TimerManager) active(ledger *model.TimerLedger, ignoreJob string) int {
	count := 0
	for _, entry := range ledger.Entries {
		if entry.JobID != "" && entry.JobID == ignoreJob {
			continue
		}
		if entry.Status == model.TimerAccepted || entry.Status == model.TimerDispatching {
			count++
		} else if entry.Status == model.TimerLaunched {
			if j, ok := m.jobs.Find(entry.JobID); ok && !j.Deleted &&
				(j.Status == model.JobStatusRunning || j.Status == model.JobStatusPending) &&
				(j.GraphStatus == model.GraphRunStatusRunning || j.GraphStatus == model.GraphRunStatusPending ||
					j.GraphStatus == model.GraphRunStatusStepStopping || j.GraphStatus == "" && j.Status == model.JobStatusRunning) {
				count++
			} else if m.resuming[entry.JobID] == ledger.ID {
				count++
			}
		}
	}
	return count
}

func (m *TimerManager) Resume(ctx context.Context, job *model.Job, resume func() error) error {
	if job.ScheduleID == "" {
		return resume()
	}
	m.mu.Lock()
	ledger, err := m.history.Load(job.ScheduleID)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if ledger == nil {
		if task, getErr := m.definitions.Get(ctx, job.ScheduleID); getErr != nil {
			m.mu.Unlock()
			return getErr
		} else if task != nil {
			ledger, err = m.loadLedger(ctx, task, time.Now())
			if err != nil {
				m.mu.Unlock()
				return err
			}
		}
	}
	limit := 1
	task, err := m.definitions.Get(ctx, job.ScheduleID)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if task != nil {
		limit = max(1, task.MaxConcurrent)
	}
	if m.resuming[job.ID] != "" || ledger != nil && m.active(ledger, job.ID) >= limit {
		m.mu.Unlock()
		return fmt.Errorf("%w (limit %d)", ErrScheduleMaxConcurrent, limit)
	}
	m.resuming[job.ID] = job.ScheduleID
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.resuming, job.ID)
		m.mu.Unlock()
	}()
	return resume()
}

func (m *TimerManager) Start(ctx context.Context) {
	m.mu.Lock()
	err := errors.Join(m.recoverLocked(ctx), m.sweep(ctx, time.Now()))
	m.mu.Unlock()
	if err != nil {
		logger.Errorf(ctx, "[timer] recover: %v", err)
	}
	go m.serve(ctx)
}

func (m *TimerManager) serve(ctx context.Context) {
	syncMinute := time.NewTimer(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
	defer syncMinute.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-syncMinute.C:
			if err := m.tick(ctx, time.Now()); err != nil {
				logger.Errorf(ctx, "[timer] tick: %v", err)
			}
			syncMinute.Reset(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
		}
	}
}

func (m *TimerManager) recoverLocked(ctx context.Context) error {
	ids, err := m.history.ListIDs()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		ledger, err := m.history.Load(id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if ledger == nil {
			continue
		}
		var accepted []string
		dirty := false
		for i := range ledger.Entries {
			entry := &ledger.Entries[i]
			switch entry.Status {
			case model.TimerAccepted:
				accepted = append(accepted, entry.ID)
			case model.TimerLaunched:
				j, ok := m.jobs.Find(entry.JobID)
				if ok && j.Mode == model.JobModeInteractive {
					state := j.ClientMessageReceipts[entry.ID].State
					if state == model.ClientMessageStateProcessing || state == model.ClientMessageStateInterrupted {
						now := time.Now()
						entry.Status, entry.FinishedAt = model.TimerInterrupted, &now
						entry.Error = "Backend restarted before the Prompt execution finished"
						dirty = true
					}
				}
			case model.TimerDispatching:
				j, ok := m.jobs.Find(m.jobs.ID(entry.ID))
				dirty = true
				if ok && !j.Deleted {
					entry.JobID = j.ID
					if j.GraphRunID != "" || j.ClientMessageReceipts[entry.ID].State != "" {
						entry.Status = model.TimerLaunched
						if j.Mode == model.JobModeInteractive && j.ClientMessageReceipts[entry.ID].State == model.ClientMessageStateProcessing {
							now := time.Now()
							entry.Status, entry.FinishedAt = model.TimerInterrupted, &now
							entry.Error = "Backend restarted before the Prompt execution finished"
						}
						continue
					}
					// Both execution paths persist their Job-side launch marker
					// before starting work. A pristine Job is safe to reuse.
					if j.ScheduleID == id && j.CreationClientMessageID == entry.ID && j.Status == model.JobStatusPending && j.StartedAt == 0 {
						entry.Status = model.TimerAccepted
						accepted = append(accepted, entry.ID)
						continue
					}
				}
				now := time.Now()
				entry.Status, entry.FinishedAt = model.TimerInterrupted, &now
				entry.Error = "Backend restarted during launch and could not confirm whether this execution already started"
			}
		}
		if dirty {
			if err := m.history.Save(ledger); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		for _, entryID := range accepted {
			m.startLocked(ctx, id, entryID)
		}
	}
	return errors.Join(failures...)
}

func (m *TimerManager) tick(ctx context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sweep(ctx, now)
}

func (m *TimerManager) sweep(ctx context.Context, now time.Time) error {
	tasks, err := m.definitions.List(ctx)
	if err != nil {
		return err
	}
	minute := now.Truncate(time.Minute)
	var failures []error
	for _, task := range tasks {
		if err := m.tickTimer(ctx, task, minute, now); err != nil {
			failures = append(failures, fmt.Errorf("timer %s: %w", task.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (m *TimerManager) tickTimer(ctx context.Context, task *model.ScheduledTask, minute, now time.Time) error {
	ledger, err := m.loadLedger(ctx, task, now)
	if err != nil || !ledger.Enabled {
		return err
	}
	target, _, err := m.inspect(ctx, task, ledger)
	if err != nil || ledger.PendingReason != "" || !minute.After(ledger.CheckedThrough) {
		return err
	}
	count := timerRunCount(ledger)
	if task.MaxRuns > 0 && count >= task.MaxRuns || task.RunAt != "" && count > 0 {
		return nil
	}
	next := nextTimerTime(task, ledger.CheckedThrough)
	if next == nil || next.After(minute) {
		return nil
	}
	through := minute.Add(-time.Minute)
	if !next.After(through) {
		firstMissed := next
		if task.RunAt != "" {
			through = *next
		} else if location, err := timerLocation(task.TimeZone); err == nil {
			if deadline, _ := timerDeadline(task.EndDate, location); !deadline.IsZero() && !through.Before(deadline) {
				through = deadline.Add(-time.Minute)
			}
		}
		ledger.Entries = append(ledger.Entries, model.TimerEntry{TimerExecution: model.TimerExecution{
			ID:         timerExecutionID(task.ID, "missed", firstMissed.UTC().Format(time.RFC3339)),
			ScheduleID: task.ID, Name: task.Name, Kind: task.Kind, WorkspaceID: task.WorkspaceID, WorkflowID: task.GraphWorkflowID,
			Trigger: "automatic", Status: model.TimerMissed, ScheduledAt: firstMissed, MissedThrough: &through,
			Error: "Scheduled opportunity elapsed before the backend accepted it",
		}})
		next = nextTimerTime(task, through)
	}
	var entryID string
	if next != nil && next.Equal(minute) && timerEndReason(task, ledger, now) == "" {
		entryID = timerExecutionID(task.ID, "automatic", minute.UTC().Format(time.RFC3339))
		if m.active(ledger, "") >= max(1, task.MaxConcurrent) {
			ledger.Entries = append(ledger.Entries, model.TimerEntry{TimerExecution: model.TimerExecution{
				ID: entryID, ScheduleID: task.ID, Name: task.Name, Kind: task.Kind, WorkspaceID: task.WorkspaceID, WorkflowID: task.GraphWorkflowID,
				Trigger: "automatic", Status: model.TimerSkipped, ScheduledAt: next, Error: ErrScheduleMaxConcurrent.Error(),
			}})
		} else {
			m.admit(ctx, ledger, task, target, entryID, "automatic", next, now)
		}
	}
	ledger.CheckedThrough = minute
	if err := m.history.Save(ledger); err != nil {
		return err
	}
	m.startLocked(ctx, task.ID, entryID)
	return nil
}

func (m *TimerManager) startLocked(ctx context.Context, scheduleID, entryID string) {
	if entryID == "" {
		return
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("timer launch panic: %v", recovered)
				if saveErr := m.recordDispatch(scheduleID, entryID, "", "", err); saveErr != nil {
					err = errors.Join(err, saveErr)
				}
				logger.Errorf(ctx, "[timer] dispatch %s/%s: %v", scheduleID, entryID, err)
			}
		}()
		if err := m.dispatch(context.WithoutCancel(ctx), scheduleID, entryID); err != nil {
			logger.Errorf(ctx, "[timer] dispatch %s/%s: %v", scheduleID, entryID, err)
		}
	}()
}

func findTimerEntry(ledger *model.TimerLedger, id string) *model.TimerEntry {
	for i := range ledger.Entries {
		if ledger.Entries[i].ID == id {
			return &ledger.Entries[i]
		}
	}
	return nil
}

func (m *TimerManager) dispatch(ctx context.Context, scheduleID, entryID string) error {
	m.mu.Lock()
	ledger, err := m.history.Load(scheduleID)
	if err != nil || ledger == nil {
		m.mu.Unlock()
		return err
	}
	entry := findTimerEntry(ledger, entryID)
	if entry == nil || entry.Status != model.TimerAccepted || entry.Target == nil {
		m.mu.Unlock()
		return nil
	}
	snapshot := *entry
	m.mu.Unlock()

	job, launchErr := m.jobs.Prepare(ctx, &snapshot)
	if launchErr == nil {
		if err := m.recordDispatch(scheduleID, entryID, job.ID, model.TimerDispatching, nil); err != nil {
			return err
		}
		launchErr = m.jobs.Launch(ctx, &snapshot, job)
	}
	jobID := ""
	if job != nil {
		jobID = job.ID
	}
	return m.recordDispatch(scheduleID, entryID, jobID, model.TimerLaunched, launchErr)
}

func (m *TimerManager) recordDispatch(scheduleID, entryID, jobID, status string, launchErr error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger, err := m.history.Load(scheduleID)
	if err != nil || ledger == nil {
		return err
	}
	entry := findTimerEntry(ledger, entryID)
	if entry == nil {
		return fmt.Errorf("execution %s disappeared from %s", entryID, scheduleID)
	}
	if entry.Status != model.TimerAccepted && entry.Status != model.TimerDispatching {
		return fmt.Errorf("%w: execution %s is already %s", ErrTimerConflict, entryID, entry.Status)
	}
	if jobID != "" {
		entry.JobID = jobID
	}
	entry.Status = status
	if launchErr != nil {
		now := time.Now()
		entry.Error, entry.Status, entry.FinishedAt = launchErr.Error(), string(model.JobStatusFailed), &now
	}
	return m.history.Save(ledger)
}

func (m *TimerManager) executionRecord(entry model.TimerEntry) model.TimerExecution {
	out := entry.TimerExecution
	if entry.Target != nil {
		cfg := entry.Target.Task
		if entry.Target.Workflow != nil {
			cfg.WorkflowVersion = entry.Target.Workflow.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		out.Configuration = &cfg
	}
	if out.JobID == "" {
		return out
	}
	j, ok := m.jobs.Find(out.JobID)
	if !ok || j.Deleted {
		out.DetailsError = fmt.Sprintf("Job %s is unavailable", out.JobID)
		if out.Status == model.TimerLaunched && entry.LastKnownStatus != "" {
			out.Status = entry.LastKnownStatus
		}
		return out
	}
	observeTimerJob(&out, j)
	return out
}

func observeTimerJob(out *model.TimerExecution, job *model.Job) {
	started, finished := job.StartedAt, job.FinishedAt
	if out.Status == model.TimerLaunched {
		out.Status = string(job.Status)
		if job.GraphStatus != "" {
			out.Status = string(job.GraphStatus)
		} else if receipt, ok := job.ClientMessageReceipts[out.ID]; ok {
			if receipt.State != model.ClientMessageStateProcessing {
				out.Status = string(receipt.State)
			}
			started, finished = receipt.AcceptedAt, receipt.FinishedAt
		}
		if job.Progress != nil && job.Progress.LastError != "" {
			out.Error = job.Progress.LastError
		}
	}
	if started != 0 {
		value := time.UnixMilli(started)
		out.StartedAt = &value
	}
	if finished != 0 {
		value := time.UnixMilli(finished)
		out.FinishedAt = &value
	}
}

// PreserveJob retains the execution summary before its Job data is removed.
func (m *TimerManager) PreserveJob(job *model.Job) error {
	if job.ScheduleID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger, err := m.history.Load(job.ScheduleID)
	if err != nil || ledger == nil {
		return err
	}
	for i := range ledger.Entries {
		entry := &ledger.Entries[i]
		if entry.JobID == job.ID || entry.ID == job.CreationClientMessageID {
			entry.JobID = job.ID
			if entry.Status == model.TimerAccepted || entry.Status == model.TimerDispatching {
				now := time.Now()
				entry.Status, entry.FinishedAt = string(model.JobStatusStopped), &now
			}
			record := entry.TimerExecution
			observeTimerJob(&record, job)
			entry.LastKnownStatus, entry.StartedAt, entry.FinishedAt, entry.Error = record.Status, record.StartedAt, record.FinishedAt, record.Error
			return m.history.Save(ledger)
		}
	}
	return nil
}

func (m *TimerManager) Executions(ctx context.Context, id, executionID, workspace, status string) ([]model.TimerExecution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := []string{id}
	if id == "" {
		var err error
		ids, err = m.history.ListIDs()
		if err != nil {
			return nil, err
		}
		tasks, err := m.definitions.List(ctx)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]bool, len(ids))
		for _, id := range ids {
			seen[id] = true
		}
		for _, task := range tasks {
			if task.Kind == "" && !seen[task.ID] {
				ids = append(ids, task.ID)
			}
		}
	}
	out := make([]model.TimerExecution, 0)
	for _, id := range ids {
		ledger, err := m.history.Load(id)
		if err != nil {
			return nil, err
		}
		if ledger == nil {
			task, err := m.definitions.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			if task == nil {
				continue
			}
			ledger, err = m.loadLedger(ctx, task, time.Now())
			if err != nil {
				return nil, err
			}
		}
		for _, entry := range ledger.Entries {
			if executionID != "" && entry.ID != executionID {
				continue
			}
			record := m.executionRecord(entry)
			if (workspace == "" || workspace == record.WorkspaceID) && (status == "" || status == record.Status) {
				out = append(out, record)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		timeI, timeJ := sortExecutionTime(out[i]), sortExecutionTime(out[j])
		return timeI.After(timeJ) || timeI.Equal(timeJ) && out[i].ID > out[j].ID
	})
	return out, nil
}

func sortExecutionTime(entry model.TimerExecution) time.Time {
	if entry.TriggeredAt != nil {
		return *entry.TriggeredAt
	}
	if entry.ScheduledAt != nil {
		return *entry.ScheduledAt
	}
	return time.Time{}
}
