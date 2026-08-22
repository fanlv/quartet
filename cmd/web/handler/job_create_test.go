package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/route"
	"github.com/fanlv/quartet/repository"
	jobsvc "github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/model"
)

type createJobWorkspaceService struct {
	workspace.Service
	workspace *model.Workspace
}

func (s *createJobWorkspaceService) Get(id string) (*model.Workspace, bool) {
	if s.workspace == nil || s.workspace.ID != id {
		return nil, false
	}
	copy := *s.workspace
	return &copy, true
}

type captureCreateJobService struct {
	jobsvc.Service
	mu      sync.Mutex
	created *model.Job
	count   int
}

func (s *captureCreateJobService) Create(job *model.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = job.DeepCopy()
	s.count++
	return nil
}

func (s *captureCreateJobService) CreateIdempotent(job *model.Job) (*model.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.created != nil {
		if s.created.ID != job.ID || s.created.CreationPayloadHash != job.CreationPayloadHash {
			return nil, false, jobsvc.ErrClientMessageIDConflict
		}
		return s.created.DeepCopy(), true, nil
	}
	s.created = job.DeepCopy()
	s.count++
	return s.created.DeepCopy(), false, nil
}

type createJobRecentDirsRepo struct {
	repository.RecentDirsRepo
}

type createJobCall struct {
	jobsvc.Service
	mu    sync.Mutex
	jobs  map[string]*model.Job
	count int
}

func (s *createJobCall) CreateIdempotent(job *model.Job) (*model.Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = make(map[string]*model.Job)
	}
	if existing := s.jobs[job.ID]; existing != nil {
		if existing.CreationPayloadHash != job.CreationPayloadHash {
			return nil, false, jobsvc.ErrClientMessageIDConflict
		}
		return existing.DeepCopy(), true, nil
	}
	s.jobs[job.ID] = job.DeepCopy()
	s.count++
	return job.DeepCopy(), false, nil
}

func TestCreateJobIdempotentReturnsSameJobForRepeatedCommandAction(t *testing.T) {
	workdir := t.TempDir()
	jobService := &captureCreateJobService{}
	h := &Handler{
		workspaceService: &createJobWorkspaceService{workspace: &model.Workspace{ID: "ws-1", Workdir: workdir}},
		jobService:       jobService,
		recentDirsRepo:   createJobRecentDirsRepo{},
	}
	req := &model.CreateJobRequest{
		ClientMessageID: "command-client-1",
		AgentType:       "claude",
		ModelID:         "sonnet",
		Mode:            model.JobModeInteractive,
		WorkspaceID:     "ws-1",
		Workdir:         workdir,
	}

	first, duplicate, err := h.createJobIdempotent(context.Background(), req)
	if err != nil || duplicate {
		t.Fatalf("first create=(job=%v duplicate=%t err=%v), want fresh success", first, duplicate, err)
	}
	second, duplicate, err := h.createJobIdempotent(context.Background(), req)
	if err != nil || !duplicate {
		t.Fatalf("retry create=(job=%v duplicate=%t err=%v), want duplicate success", second, duplicate, err)
	}
	if first.ID != second.ID {
		t.Fatalf("retry jobId=%q, want original %q", second.ID, first.ID)
	}
	jobService.mu.Lock()
	defer jobService.mu.Unlock()
	if jobService.count != 1 {
		t.Fatalf("persisted jobs=%d, want 1", jobService.count)
	}
}

func TestJobCreateHTTPRepeatedCommandActionReturnsSameJobID(t *testing.T) {
	workdir := t.TempDir()
	jobService := &captureCreateJobService{}
	h := &Handler{
		workspaceService: &createJobWorkspaceService{workspace: &model.Workspace{ID: "ws-1", Workdir: workdir}},
		jobService:       jobService,
		recentDirsRepo:   createJobRecentDirsRepo{},
	}
	engine := route.NewEngine(config.NewOptions(nil))
	engine.POST("/api/v1/job/create", h.JobCreate)
	req := model.CreateJobRequest{
		ClientMessageID: commandActionClientMessageID("source-job", "command-client-http"),
		AgentType:       "claude",
		ModelID:         "sonnet",
		Mode:            model.JobModeInteractive,
		WorkspaceID:     "ws-1",
		Workdir:         workdir,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	perform := func() model.CreateJobResponse {
		recorder := ut.PerformRequest(engine, http.MethodPost, "/api/v1/job/create",
			&ut.Body{Body: bytes.NewReader(body), Len: len(body)},
			ut.Header{Key: "Content-Type", Value: "application/json"})
		resp := recorder.Result()
		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode(), resp.Body())
		}
		var decoded model.CreateJobResponse
		if err := json.Unmarshal(resp.Body(), &decoded); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return decoded
	}
	first := perform()
	second := perform()
	if first.Status != "created" || second.Status != "duplicate" {
		t.Fatalf("statuses=(%q,%q), want created/duplicate", first.Status, second.Status)
	}
	if first.JobID == "" || second.JobID != first.JobID {
		t.Fatalf("job IDs=(%q,%q), want same non-empty ID", first.JobID, second.JobID)
	}
	jobService.mu.Lock()
	defer jobService.mu.Unlock()
	if jobService.count != 1 {
		t.Fatalf("persisted jobs=%d, want 1", jobService.count)
	}
}

func TestJobCreateHTTPConcurrentRepeatedCommandActionCreatesOnce(t *testing.T) {
	workdir := t.TempDir()
	jobs := &createJobCall{}
	h := &Handler{
		workspaceService: &createJobWorkspaceService{workspace: &model.Workspace{ID: "ws-1", Workdir: workdir}},
		jobService:       jobs,
		recentDirsRepo:   createJobRecentDirsRepo{},
	}
	engine := route.NewEngine(config.NewOptions(nil))
	engine.POST("/api/v1/job/create", h.JobCreate)
	req := model.CreateJobRequest{ClientMessageID: "command-concurrent", AgentType: "claude", WorkspaceID: "ws-1", Workdir: workdir}
	body, _ := json.Marshal(req)
	start := make(chan struct{})
	responses := make(chan model.CreateJobResponse, 2)
	for range 2 {
		go func() {
			<-start
			recorder := ut.PerformRequest(engine, http.MethodPost, "/api/v1/job/create",
				&ut.Body{Body: bytes.NewReader(body), Len: len(body)}, ut.Header{Key: "Content-Type", Value: "application/json"})
			var decoded model.CreateJobResponse
			if recorder.Result().StatusCode() == http.StatusOK {
				_ = json.Unmarshal(recorder.Result().Body(), &decoded)
			}
			responses <- decoded
		}()
	}
	close(start)
	first, second := <-responses, <-responses
	if first.JobID == "" || first.JobID != second.JobID {
		t.Fatalf("concurrent job IDs=(%q,%q), want same non-empty ID", first.JobID, second.JobID)
	}
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	if jobs.count != 1 {
		t.Fatalf("persisted jobs=%d, want 1", jobs.count)
	}
}

func TestCreateJobIdempotentRejectsSameIDWithDifferentPayload(t *testing.T) {
	workdir := t.TempDir()
	jobService := &captureCreateJobService{}
	h := &Handler{
		workspaceService: &createJobWorkspaceService{workspace: &model.Workspace{ID: "ws-1", Workdir: workdir}},
		jobService:       jobService,
		recentDirsRepo:   createJobRecentDirsRepo{},
	}
	first := &model.CreateJobRequest{ClientMessageID: "command-client-1", AgentType: "claude", ModelID: "sonnet", WorkspaceID: "ws-1", Workdir: workdir}
	if _, _, err := h.createJobIdempotent(context.Background(), first); err != nil {
		t.Fatalf("first create: %v", err)
	}
	conflict := *first
	conflict.ModelID = "opus"
	if _, _, err := h.createJobIdempotent(context.Background(), &conflict); !errors.Is(err, jobsvc.ErrClientMessageIDConflict) {
		t.Fatalf("conflicting create error=%v, want ErrClientMessageIDConflict", err)
	}
}

func (createJobRecentDirsRepo) Add(context.Context, string) error { return nil }

func TestCreateJobPersistsInitialInteractiveConfiguration(t *testing.T) {
	workdir := t.TempDir()
	jobService := &captureCreateJobService{}
	h := &Handler{
		workspaceService: &createJobWorkspaceService{workspace: &model.Workspace{
			ID:      "ws-ios",
			Workdir: workdir,
		}},
		jobService:     jobService,
		recentDirsRepo: createJobRecentDirsRepo{},
	}
	req := &model.CreateJobRequest{
		AgentType:       "claude",
		ModelID:         "claude-sonnet",
		ACPMode:         "plan",
		ACPThoughtLevel: "high",
		Mode:            model.JobModeInteractive,
		WorkspaceID:     "ws-ios",
		Workdir:         workdir,
	}

	created, err := h.createJob(context.Background(), req)
	if err != nil {
		t.Fatalf("createJob failed: %v", err)
	}
	for name, values := range map[string][2]string{
		"agent":         {created.InitialAgentID, req.AgentType},
		"model":         {created.FirstModelID, req.ModelID},
		"mode":          {created.InitialACPMode, req.ACPMode},
		"thought level": {created.InitialACPThoughtLevel, req.ACPThoughtLevel},
	} {
		if values[0] != values[1] {
			t.Errorf("%s = %q, want %q", name, values[0], values[1])
		}
	}
	if jobService.created == nil {
		t.Fatal("job service did not receive the new Job")
	}
	if jobService.created.InitialAgentID != req.AgentType || jobService.created.FirstModelID != req.ModelID {
		t.Fatalf("persisted Job lost initial configuration: %+v", jobService.created)
	}
}
