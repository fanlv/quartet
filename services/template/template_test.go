package template

import (
	"context"
	"errors"
	"testing"

	"github.com/fanlv/quartet/types/model"
)

type stubTemplateRepo struct {
	deleteCalled bool
	deleteErr    error
}

func (s *stubTemplateRepo) Save(context.Context, *model.LoopTemplate) error { return nil }

func (s *stubTemplateRepo) Get(context.Context, string) (*model.LoopTemplate, error) {
	return nil, nil
}

func (s *stubTemplateRepo) Update(context.Context, string, *model.LoopTemplate) error { return nil }

func (s *stubTemplateRepo) List(context.Context) ([]*model.LoopTemplate, error) { return nil, nil }

func (s *stubTemplateRepo) Delete(context.Context, string) error {
	s.deleteCalled = true
	return s.deleteErr
}

type stubScheduleRepo struct {
	tasks []*model.ScheduledTask
	err   error
}

func (s *stubScheduleRepo) Save(context.Context, *model.ScheduledTask) error { return nil }

func (s *stubScheduleRepo) Get(context.Context, string) (*model.ScheduledTask, error) {
	return nil, nil
}

func (s *stubScheduleRepo) List(context.Context) ([]*model.ScheduledTask, error) {
	return s.tasks, s.err
}

func (s *stubScheduleRepo) Delete(context.Context, string) error { return nil }

func TestDeleteRejectsReferencedTemplate(t *testing.T) {
	tmplRepo := &stubTemplateRepo{}
	svc := &serviceImpl{
		repo: tmplRepo,
		scheduleRepo: &stubScheduleRepo{tasks: []*model.ScheduledTask{
			{ID: "sch-1", Name: "daily job", TemplateID: "tmpl-1"},
		}},
	}

	err := svc.Delete(context.Background(), "tmpl-1")
	if !errors.Is(err, ErrTemplateReferenced) {
		t.Fatalf("expected ErrTemplateReferenced, got %v", err)
	}
	if tmplRepo.deleteCalled {
		t.Fatal("expected template repo delete not to be called")
	}
}

func TestDeleteAllowsUnreferencedTemplate(t *testing.T) {
	tmplRepo := &stubTemplateRepo{}
	svc := &serviceImpl{
		repo:         tmplRepo,
		scheduleRepo: &stubScheduleRepo{tasks: []*model.ScheduledTask{{ID: "sch-1", TemplateID: "tmpl-2"}}},
	}

	if err := svc.Delete(context.Background(), "tmpl-1"); err != nil {
		t.Fatalf("expected delete success, got %v", err)
	}
	if !tmplRepo.deleteCalled {
		t.Fatal("expected template repo delete to be called")
	}
}
