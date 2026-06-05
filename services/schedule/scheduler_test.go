package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

func TestCronMatches(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		time   time.Time
		expect bool
	}{
		// Wildcard
		{"every minute", "* * * * *", time.Date(2026, 4, 11, 10, 30, 0, 0, time.Local), true},

		// Exact values
		{"exact minute match", "30 * * * *", time.Date(2026, 4, 11, 10, 30, 0, 0, time.Local), true},
		{"exact minute no match", "15 * * * *", time.Date(2026, 4, 11, 10, 30, 0, 0, time.Local), false},
		{"exact hour match", "0 9 * * *", time.Date(2026, 4, 11, 9, 0, 0, 0, time.Local), true},
		{"exact hour no match", "0 9 * * *", time.Date(2026, 4, 11, 10, 0, 0, 0, time.Local), false},
		{"exact day match", "0 0 15 * *", time.Date(2026, 4, 15, 0, 0, 0, 0, time.Local), true},
		{"exact month match", "0 0 1 6 *", time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local), true},

		// Step
		{"step */5 match at 0", "*/5 * * * *", time.Date(2026, 4, 11, 10, 0, 0, 0, time.Local), true},
		{"step */5 match at 15", "*/5 * * * *", time.Date(2026, 4, 11, 10, 15, 0, 0, time.Local), true},
		{"step */5 no match at 13", "*/5 * * * *", time.Date(2026, 4, 11, 10, 13, 0, 0, time.Local), false},
		{"step */15 match at 45", "*/15 * * * *", time.Date(2026, 4, 11, 10, 45, 0, 0, time.Local), true},

		// Range
		{"range 1-5 match", "* * 1-5 * *", time.Date(2026, 4, 3, 10, 0, 0, 0, time.Local), true},
		{"range 1-5 no match", "* * 1-5 * *", time.Date(2026, 4, 6, 10, 0, 0, 0, time.Local), false},
		{"range boundary low", "* * 1-5 * *", time.Date(2026, 4, 1, 10, 0, 0, 0, time.Local), true},
		{"range boundary high", "* * 1-5 * *", time.Date(2026, 4, 5, 10, 0, 0, 0, time.Local), true},

		// Range with step
		{"range+step 1-10/3 match at 1", "1-10/3 * * * *", time.Date(2026, 4, 11, 10, 1, 0, 0, time.Local), true},
		{"range+step 1-10/3 match at 4", "1-10/3 * * * *", time.Date(2026, 4, 11, 10, 4, 0, 0, time.Local), true},
		{"range+step 1-10/3 match at 7", "1-10/3 * * * *", time.Date(2026, 4, 11, 10, 7, 0, 0, time.Local), true},
		{"range+step 1-10/3 no match at 2", "1-10/3 * * * *", time.Date(2026, 4, 11, 10, 2, 0, 0, time.Local), false},
		{"range+step 1-10/3 no match at 11", "1-10/3 * * * *", time.Date(2026, 4, 11, 10, 11, 0, 0, time.Local), false},

		// Comma list
		{"comma 0,15,30,45 match", "0,15,30,45 * * * *", time.Date(2026, 4, 11, 10, 15, 0, 0, time.Local), true},
		{"comma 0,15,30,45 no match", "0,15,30,45 * * * *", time.Date(2026, 4, 11, 10, 20, 0, 0, time.Local), false},

		// Day of week (0=Sunday)
		{"weekday monday=1", "0 9 * * 1", time.Date(2026, 4, 13, 9, 0, 0, 0, time.Local), true},        // April 13, 2026 is Monday
		{"weekday sunday=0", "0 9 * * 0", time.Date(2026, 4, 12, 9, 0, 0, 0, time.Local), true},        // April 12, 2026 is Sunday
		{"weekday wrong day", "0 9 * * 1", time.Date(2026, 4, 12, 9, 0, 0, 0, time.Local), false},      // Sunday != Monday
		{"weekdays 1-5", "0 9 * * 1-5", time.Date(2026, 4, 14, 9, 0, 0, 0, time.Local), true},          // Tuesday
		{"weekdays 1-5 weekend", "0 9 * * 1-5", time.Date(2026, 4, 12, 9, 0, 0, 0, time.Local), false}, // Sunday

		// All fields combined
		{"full match", "30 14 11 4 *", time.Date(2026, 4, 11, 14, 30, 0, 0, time.Local), true},
		{"full no match minute", "30 14 11 4 *", time.Date(2026, 4, 11, 14, 31, 0, 0, time.Local), false},

		// Invalid expressions
		{"invalid 6 fields", "* * * * * *", time.Date(2026, 4, 11, 10, 0, 0, 0, time.Local), false},
		{"invalid 4 fields", "* * * *", time.Date(2026, 4, 11, 10, 0, 0, 0, time.Local), false},
		{"empty", "", time.Date(2026, 4, 11, 10, 0, 0, 0, time.Local), false},
		{"invalid value", "abc * * * *", time.Date(2026, 4, 11, 10, 0, 0, 0, time.Local), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cronMatches(tt.expr, tt.time)
			if got != tt.expect {
				t.Errorf("cronMatches(%q, %v) = %v, want %v", tt.expr, tt.time, got, tt.expect)
			}
		})
	}
}

func TestNextCronTime(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		after  time.Time
		expect time.Time
		isNil  bool
	}{
		{
			"every minute",
			"* * * * *",
			time.Date(2026, 4, 11, 10, 30, 0, 0, time.Local),
			time.Date(2026, 4, 11, 10, 31, 0, 0, time.Local),
			false,
		},
		{
			"next hour",
			"0 * * * *",
			time.Date(2026, 4, 11, 10, 30, 0, 0, time.Local),
			time.Date(2026, 4, 11, 11, 0, 0, 0, time.Local),
			false,
		},
		{
			"next day at 9am",
			"0 9 * * *",
			time.Date(2026, 4, 11, 10, 0, 0, 0, time.Local),
			time.Date(2026, 4, 12, 9, 0, 0, 0, time.Local),
			false,
		},
		{
			"same minute advances",
			"30 10 * * *",
			time.Date(2026, 4, 11, 10, 30, 0, 0, time.Local),
			// After is truncated to minute + 1 min, so the earliest match is the next day
			time.Date(2026, 4, 12, 10, 30, 0, 0, time.Local),
			false,
		},
		{
			"specific date",
			"0 0 25 12 *",
			time.Date(2026, 4, 11, 0, 0, 0, 0, time.Local),
			time.Date(2026, 12, 25, 0, 0, 0, 0, time.Local),
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextCronTime(tt.expr, tt.after)
			if tt.isNil {
				if got != nil {
					t.Errorf("NextCronTime(%q, %v) = %v, want nil", tt.expr, tt.after, *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("NextCronTime(%q, %v) = nil, want %v", tt.expr, tt.after, tt.expect)
			}
			if !got.Equal(tt.expect) {
				t.Errorf("NextCronTime(%q, %v) = %v, want %v", tt.expr, tt.after, *got, tt.expect)
			}
		})
	}
}

func TestValidateCronExpr(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"valid every minute", "* * * * *", false},
		{"valid specific", "30 14 1 6 3", false},
		{"valid step", "*/5 * * * *", false},
		{"valid range", "0 9-17 * * 1-5", false},
		{"valid comma", "0,15,30,45 * * * *", false},
		{"valid range+step", "0-30/5 * * * *", false},

		// Invalid
		{"too few fields", "* * * *", true},
		{"too many fields", "* * * * * *", true},
		{"minute out of range", "60 * * * *", true},
		{"hour out of range", "0 24 * * *", true},
		{"day out of range", "0 0 32 * *", true},
		{"month out of range", "0 0 * 13 *", true},
		{"dow out of range", "0 0 * * 7", true},
		{"invalid step", "*/0 * * * *", true},
		{"invalid range reversed", "0 17-9 * * *", true},
		{"non-numeric", "abc * * * *", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCronExpr(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCronExpr(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}

// fakeService is an in-memory Service for scheduler unit tests. Only the
// methods RecordTrigger touches (Get, Save) are implemented.
type fakeService struct {
	tasks map[string]*model.ScheduledTask
}

func newFakeService(task *model.ScheduledTask) *fakeService {
	return &fakeService{tasks: map[string]*model.ScheduledTask{task.ID: task}}
}

func (f *fakeService) Get(_ context.Context, id string) (*model.ScheduledTask, error) {
	t, ok := f.tasks[id]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (f *fakeService) Save(_ context.Context, task *model.ScheduledTask) error {
	f.tasks[task.ID] = task
	return nil
}

func (f *fakeService) Create(context.Context, *model.CreateScheduleRequest) (*model.ScheduledTask, error) {
	return nil, nil
}
func (f *fakeService) List(context.Context) ([]*model.ScheduledTask, error) { return nil, nil }
func (f *fakeService) ListByWorkspace(context.Context, string) ([]*model.ScheduledTask, error) {
	return nil, nil
}
func (f *fakeService) Update(context.Context, string, *model.UpdateScheduleRequest) (*model.ScheduledTask, error) {
	return nil, nil
}
func (f *fakeService) Delete(context.Context, string) error { return nil }

// TestRecordTrigger_NewJobAfterTerminalStatus reproduces the bug where
// LastStatus from a previous terminal run leaked into a fresh run, leaving
// the schedule showing e.g. "completed" while the new job was still running.
func TestRecordTrigger_NewJobAfterTerminalStatus(t *testing.T) {
	cases := []struct {
		name      string
		prevState model.JobStatus
	}{
		{"prev completed", model.JobStatusCompleted},
		{"prev failed", model.JobStatusFailed},
		{"prev stopped", model.JobStatusStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &model.ScheduledTask{
				ID:           "sch-test",
				CronExpr:     "* * * * *",
				LastRunJobID: "job-old",
				LastStatus:   tc.prevState,
			}
			svc := newFakeService(task)
			s := NewScheduler(svc, nil)

			s.RecordTrigger(context.Background(), task.ID, "job-new", "* * * * *", nil)

			got, _ := svc.Get(context.Background(), task.ID)
			if got.LastRunJobID != "job-new" {
				t.Errorf("LastRunJobID = %q, want %q", got.LastRunJobID, "job-new")
			}
			if got.LastStatus != model.JobStatusRunning {
				t.Errorf("LastStatus = %q, want %q (must not leak previous %q)",
					got.LastStatus, model.JobStatusRunning, tc.prevState)
			}
		})
	}
}

// TestRecordTrigger_PreserveSameJobTerminalStatus protects the original
// race fix: when MarkDone for a fast-completing job lands before
// RecordTrigger, the terminal status it wrote must not be overwritten.
func TestRecordTrigger_PreserveSameJobTerminalStatus(t *testing.T) {
	task := &model.ScheduledTask{
		ID:           "sch-test",
		CronExpr:     "* * * * *",
		LastRunJobID: "job-fast",
		LastStatus:   model.JobStatusCompleted,
	}
	svc := newFakeService(task)
	s := NewScheduler(svc, nil)

	s.RecordTrigger(context.Background(), task.ID, "job-fast", "* * * * *", nil)

	got, _ := svc.Get(context.Background(), task.ID)
	if got.LastStatus != model.JobStatusCompleted {
		t.Errorf("LastStatus = %q, want preserved %q", got.LastStatus, model.JobStatusCompleted)
	}
}

// TestRecordTrigger_TriggerError ensures a trigger failure records Failed
// without touching LastRunJobID (no new job exists to point at).
func TestRecordTrigger_TriggerError(t *testing.T) {
	task := &model.ScheduledTask{
		ID:           "sch-test",
		CronExpr:     "* * * * *",
		LastRunJobID: "job-prev",
		LastStatus:   model.JobStatusCompleted,
	}
	svc := newFakeService(task)
	s := NewScheduler(svc, nil)

	s.RecordTrigger(context.Background(), task.ID, "", "* * * * *", context.DeadlineExceeded)

	got, _ := svc.Get(context.Background(), task.ID)
	if got.LastStatus != model.JobStatusFailed {
		t.Errorf("LastStatus = %q, want %q", got.LastStatus, model.JobStatusFailed)
	}
	if got.LastRunJobID != "job-prev" {
		t.Errorf("LastRunJobID = %q, want unchanged %q", got.LastRunJobID, "job-prev")
	}
	if got.RunCount != 1 {
		t.Errorf("RunCount = %d, want 1", got.RunCount)
	}
}

// TestRecordTrigger_FirstRun covers the empty-state path where a schedule
// has never fired before.
func TestRecordTrigger_FirstRun(t *testing.T) {
	task := &model.ScheduledTask{ID: "sch-test", CronExpr: "* * * * *"}
	svc := newFakeService(task)
	s := NewScheduler(svc, nil)

	s.RecordTrigger(context.Background(), task.ID, "job-first", "* * * * *", nil)

	got, _ := svc.Get(context.Background(), task.ID)
	if got.LastRunJobID != "job-first" {
		t.Errorf("LastRunJobID = %q, want %q", got.LastRunJobID, "job-first")
	}
	if got.LastStatus != model.JobStatusRunning {
		t.Errorf("LastStatus = %q, want %q", got.LastStatus, model.JobStatusRunning)
	}
	if got.LastRunAt == nil || time.Since(*got.LastRunAt) > time.Minute {
		t.Errorf("LastRunAt not updated to recent time: %v", got.LastRunAt)
	}
}
