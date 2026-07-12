package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

type stubScheduleRepo struct {
	tasks map[string]*model.ScheduledTask
}

func (s *stubScheduleRepo) Save(_ context.Context, task *model.ScheduledTask) error {
	s.tasks[task.ID] = task
	return nil
}

func (s *stubScheduleRepo) SaveState(_ context.Context, task *model.ScheduledTask) error {
	s.tasks[task.ID] = task
	return nil
}

func (s *stubScheduleRepo) Get(_ context.Context, id string) (*model.ScheduledTask, error) {
	return s.tasks[id], nil
}

func (s *stubScheduleRepo) List(context.Context) ([]*model.ScheduledTask, error) {
	tasks := make([]*model.ScheduledTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (s *stubScheduleRepo) Delete(_ context.Context, id string) error {
	delete(s.tasks, id)
	return nil
}

type stubGraphWorkflowRepo struct {
	workflows map[string]*model.GraphWorkflow
}

func (s *stubGraphWorkflowRepo) Save(context.Context, *model.GraphWorkflow) error { return nil }

func (s *stubGraphWorkflowRepo) Get(_ context.Context, id string) (*model.GraphWorkflow, error) {
	return s.workflows[id], nil
}

func (s *stubGraphWorkflowRepo) List(context.Context) ([]*model.GraphWorkflow, []model.GraphWorkflowWarning, error) {
	return nil, nil, nil
}

func (s *stubGraphWorkflowRepo) Update(context.Context, string, *model.GraphWorkflow) error {
	return nil
}

func (s *stubGraphWorkflowRepo) UpdateIfUnchanged(context.Context, string, *model.GraphWorkflow, *time.Time) error {
	return nil
}

func (s *stubGraphWorkflowRepo) Delete(context.Context, string, *time.Time) error { return nil }

func TestUpdateCanChangeAndClearScheduleWorkspace(t *testing.T) {
	task := &model.ScheduledTask{
		ID:              "sch-test",
		Name:            "nightly",
		CronExpr:        "0 9 * * *",
		Enabled:         true,
		GraphWorkflowID: "wf-1",
		WorkspaceID:     "ws-old",
		Workdir:         "/tmp/old",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	svc := &serviceImpl{
		repo: &stubScheduleRepo{tasks: map[string]*model.ScheduledTask{task.ID: task}},
		graphRepo: &stubGraphWorkflowRepo{workflows: map[string]*model.GraphWorkflow{
			"wf-1": {ID: "wf-1", Name: "wf"},
		}},
	}

	nextWorkspace := "ws-new"
	clearWorkdir := ""
	updated, err := svc.Update(context.Background(), task.ID, &model.UpdateScheduleRequest{
		WorkspaceID: &nextWorkspace,
		Workdir:     &clearWorkdir,
	})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if updated.WorkspaceID != nextWorkspace {
		t.Fatalf("WorkspaceID = %q, want %q", updated.WorkspaceID, nextWorkspace)
	}
	if updated.Workdir != "" {
		t.Fatalf("Workdir = %q, want cleared", updated.Workdir)
	}

	clearWorkspace := ""
	updated, err = svc.Update(context.Background(), task.ID, &model.UpdateScheduleRequest{
		WorkspaceID: &clearWorkspace,
	})
	if err != nil {
		t.Fatalf("Update clear workspace returned error: %v", err)
	}
	if updated.WorkspaceID != "" {
		t.Fatalf("WorkspaceID = %q, want cleared", updated.WorkspaceID)
	}
}
