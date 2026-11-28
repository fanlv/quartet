package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/fanlv/quartet/types/consts"
	"os"
	"strings"
	"time"

	"github.com/fanlv/quartet/types/model"
)

func normalizeTimer(t *model.ScheduledTask) error {
	t.Name = strings.TrimSpace(t.Name)
	t.CronExpr = strings.Join(strings.Fields(t.CronExpr), " ")
	t.GraphWorkflowID = strings.TrimSpace(t.GraphWorkflowID)
	t.AgentID = strings.TrimSpace(t.AgentID)
	t.ModelID = strings.TrimSpace(t.ModelID)
	t.Workdir = strings.TrimSpace(t.Workdir)
	t.WorkspaceID = strings.TrimSpace(t.WorkspaceID)
	t.TimeZone = strings.TrimSpace(t.TimeZone)
	if t.Name == "" {
		return fmt.Errorf("timer name is required")
	}
	if t.Kind == "" {
		t.Kind = model.TimerGraph
	}
	switch t.Kind {
	case model.TimerGraph:
		if t.GraphWorkflowID == "" {
			return ErrGraphWorkflowRequired
		}
		t.Prompt, t.AgentID, t.ModelID = "", "", ""
		t.Timeout = 0
	case model.TimerPrompt:
		if strings.TrimSpace(t.Prompt) == "" {
			return fmt.Errorf("prompt is required")
		}
		t.GraphWorkflowID = ""
	default:
		return fmt.Errorf("unknown timer kind %q", t.Kind)
	}
	if t.MaxConcurrent < 0 || t.MaxRuns < 0 || t.Timeout < 0 || t.Timeout > 525600 {
		return fmt.Errorf("invalid concurrent limit, execution limit or timeout (maximum timeout: 525600 minutes)")
	}
	if t.MaxConcurrent == 0 {
		t.MaxConcurrent = 1
	}
	if t.TimeZone == "" {
		t.TimeZone = defaultTimerZone()
	}
	location, err := timerLocation(t.TimeZone)
	if err != nil {
		return err
	}
	if t.RunAt != "" {
		when, err := timerInstant(t.RunAt, location)
		if err != nil {
			return err
		}
		t.RunAt = when.UTC().Format(time.RFC3339)
		t.CronExpr, t.EndDate, t.MaxRuns = "", "", 0
		return nil
	}
	if err := ValidateCronExpr(t.CronExpr); err != nil {
		return err
	}
	_, err = timerDeadline(t.EndDate, location)
	return err
}

func defaultTimerZone() string {
	if name := strings.TrimPrefix(os.Getenv(consts.EnvKeyTimeZone), ":"); name != "" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return time.Local.String()
}

func timerLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

func timerInstant(value string, location *time.Location) (time.Time, error) {
	when, err := time.Parse(time.RFC3339, value)
	if err != nil {
		const layout = "2006-01-02T15:04"
		when, err = time.ParseInLocation(layout, value, location)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid timer time %q: %w", value, err)
		}
		if when.Format(layout) != value {
			return time.Time{}, fmt.Errorf("timer time %q does not exist in %s", value, location)
		}
	}
	if when.Second() != 0 || when.Nanosecond() != 0 {
		return time.Time{}, fmt.Errorf("timer time %q must use minute precision", value)
	}
	return when, nil
}

func timerDeadline(date string, location *time.Location) (time.Time, error) {
	if date == "" {
		return time.Time{}, nil
	}
	day, err := time.ParseInLocation(time.DateOnly, date, location)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid end date %q: %w", date, err)
	}
	return day.AddDate(0, 0, 1), nil
}

func nextTimerTime(t *model.ScheduledTask, after time.Time) *time.Time {
	location, err := timerLocation(t.TimeZone)
	if err != nil {
		return nil
	}
	if t.RunAt != "" {
		when, err := timerInstant(t.RunAt, location)
		if err == nil && when.After(after) {
			return &when
		}
		return nil
	}
	deadline, _ := timerDeadline(t.EndDate, location)
	when := after.Truncate(time.Minute).Add(time.Minute).In(location)
	limit := when.AddDate(8, 0, 0)
	if !deadline.IsZero() && deadline.Before(limit) {
		limit = deadline
	}
	fields := strings.Fields(t.CronExpr)
	if len(fields) != 5 {
		return nil
	}
	for when.Before(limit) {
		if fieldMatches(fields[3], int(when.Month()), 1, 12) &&
			fieldMatches(fields[2], when.Day(), 1, 31) &&
			fieldMatches(fields[4], int(when.Weekday()), 0, 6) &&
			fieldMatches(fields[1], when.Hour(), 0, 23) {
			if fieldMatches(fields[0], when.Minute(), 0, 59) {
				return &when
			}
			when = when.Add(time.Minute)
		} else {
			nextHour := when.Add(time.Duration(60-when.Minute()) * time.Minute)
			_, transition := when.ZoneBounds()
			if transition.After(when) && transition.Before(nextHour) {
				nextHour = transition
			}
			when = nextHour
		}
	}
	return nil
}

func timerRunCount(ledger *model.TimerLedger) int {
	count := ledger.PreviousCount
	for _, entry := range ledger.Entries {
		if entry.TriggeredAt != nil {
			count++
		}
	}
	return count
}

func timerEndReason(t *model.ScheduledTask, ledger *model.TimerLedger, now time.Time) string {
	count := timerRunCount(ledger)
	if t.RunAt != "" {
		if count > 0 {
			return "Single execution accepted"
		}
		if nextTimerTime(t, now.Truncate(time.Minute).Add(-time.Nanosecond)) == nil {
			return "Scheduled time has passed"
		}
		return ""
	}
	if t.MaxRuns > 0 && count >= t.MaxRuns {
		return "Execution limit reached"
	}
	location, err := timerLocation(t.TimeZone)
	if err == nil {
		deadline, _ := timerDeadline(t.EndDate, location)
		if !deadline.IsZero() && !now.Before(deadline) {
			return "End date reached"
		}
	}
	return ""
}

func timerFingerprint(target *model.TimerTarget) (string, error) {
	t := target.Task
	value := struct {
		Spec                                                  model.TimerSpec
		Cron, WorkflowID, WorkspaceID, Workdir, AgentRevision string
		Concurrent, Timeout                                   int
		Workflow                                              *model.GraphWorkflow
	}{t.TimerSpec, t.CronExpr, t.GraphWorkflowID, t.WorkspaceID, t.Workdir, target.AgentRevision, t.MaxConcurrent, t.Timeout, target.Workflow}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func timerTaskFromRequest(req *model.CreateScheduleRequest, now time.Time) model.ScheduledTask {
	return model.ScheduledTask{
		TimerSpec: req.TimerSpec,
		Name:      req.Name, CronExpr: req.CronExpr, GraphWorkflowID: req.GraphWorkflowID,
		WorkspaceID: req.WorkspaceID, Workdir: req.Workdir, MaxConcurrent: req.MaxConcurrent,
		Timeout: req.Timeout, CreatedAt: now, UpdatedAt: now,
	}
}

func patchTimer(t *model.ScheduledTask, req *model.UpdateScheduleRequest) {
	for _, field := range []struct {
		value *string
		field *string
	}{
		{req.Name, &t.Name}, {req.CronExpr, &t.CronExpr}, {req.GraphWorkflowID, &t.GraphWorkflowID},
		{req.WorkspaceID, &t.WorkspaceID}, {req.Workdir, &t.Workdir}, {req.Kind, &t.Kind},
		{req.Prompt, &t.Prompt}, {req.AgentID, &t.AgentID}, {req.ModelID, &t.ModelID},
		{req.RunAt, &t.RunAt}, {req.TimeZone, &t.TimeZone}, {req.EndDate, &t.EndDate},
	} {
		if field.value != nil {
			*field.field = *field.value
		}
	}
	for _, field := range []struct {
		value *int
		field *int
	}{
		{req.Timeout, &t.Timeout}, {req.MaxConcurrent, &t.MaxConcurrent}, {req.MaxRuns, &t.MaxRuns},
	} {
		if field.value != nil {
			*field.field = *field.value
		}
	}
}

func timerDefinitionFingerprint(t *model.ScheduledTask) (string, error) {
	return timerFingerprint(&model.TimerTarget{Task: *t})
}
