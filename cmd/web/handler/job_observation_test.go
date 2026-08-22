package handler

import (
	"context"
	"fmt"
	"testing"

	graphsvc "github.com/fanlv/quartet/services/graph"
	jobsvc "github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/model"
)

type observationHandlerJobService struct {
	jobsvc.Service
	jobs map[string]*model.Job
}

func (s observationHandlerJobService) Get(jobID string) (*model.Job, bool) {
	job, ok := s.jobs[jobID]
	return job, ok
}

type observationHandlerGraphService struct {
	graphsvc.Service
	statuses map[string]*model.GraphRunStatusResponse
	calls    []string
}

func (s *observationHandlerGraphService) GetRunStatus(_ context.Context, runID string) (*model.GraphRunStatusResponse, error) {
	s.calls = append(s.calls, runID)
	return s.statuses[runID], nil
}

func TestEnrichLegacyGraphObservationsPreservesExactHistoricalStates(t *testing.T) {
	graphService := &observationHandlerGraphService{statuses: map[string]*model.GraphRunStatusResponse{
		"run-exact": {Run: &model.GraphRun{Status: model.GraphRunStatusCompleted}},
		"run-legacy": {
			Run: &model.GraphRun{Status: model.GraphRunStatusAwaitingInput, FinishedAt: 22},
			Instances: []model.GraphInstanceState{{
				Status:           model.GraphInstanceStatusAwaitingInput,
				DisplaySessionID: "display-session",
			}},
		},
	}}
	handler := &Handler{
		jobService: observationHandlerJobService{jobs: map[string]*model.Job{
			"exact":  {ID: "exact", Mode: model.JobModeGraph, GraphRunID: "run-exact"},
			"legacy": {ID: "legacy", Mode: model.JobModeGraph, GraphRunID: "run-legacy"},
		}},
		graphService: graphService,
	}
	response := model.JobObservationResponse{Changes: []model.JobObservationEvent{
		{EventID: "1", Job: model.JobSummary{ID: "exact", Mode: model.JobModeGraph, Status: model.JobStatusStopped}, GraphRunID: "run-exact", GraphStatus: "awaitingInput", GraphSessionID: "historical-session"},
		{EventID: "2", Job: model.JobSummary{ID: "legacy", Mode: model.JobModeGraph, Status: model.JobStatusStopped}, GraphRunID: "run-legacy"},
	}}

	handler.enrichLegacyGraphObservations(context.Background(), &response)

	if got := response.Changes[0]; got.GraphStatus != "awaitingInput" || got.GraphSessionID != "historical-session" {
		t.Fatalf("exact historical event was overwritten: %#v", got)
	}
	if len(graphService.calls) != 1 || graphService.calls[0] != "run-legacy" {
		t.Fatalf("Graph status calls = %v, want only run-legacy", graphService.calls)
	}
	if got := response.Changes[1]; got.GraphStatus != "awaitingInput" || got.GraphSessionID != "display-session" || got.OccurredAt != 22 {
		t.Fatalf("legacy Graph event not enriched: %#v", got)
	}
}

func TestEnrichLegacyGraphObservationsBoundsFallbackLookups(t *testing.T) {
	graphService := &observationHandlerGraphService{statuses: make(map[string]*model.GraphRunStatusResponse)}
	jobs := make(map[string]*model.Job)
	response := model.JobObservationResponse{}
	for index := 0; index < 40; index++ {
		jobID := fmt.Sprintf("job-%02d", index)
		runID := fmt.Sprintf("run-%02d", index)
		jobs[jobID] = &model.Job{ID: jobID, Mode: model.JobModeGraph, GraphRunID: runID}
		graphService.statuses[runID] = &model.GraphRunStatusResponse{Run: &model.GraphRun{Status: model.GraphRunStatusStopped}}
		response.Changes = append(response.Changes, model.JobObservationEvent{
			EventID: fmt.Sprintf("event-%02d", index), GraphRunID: runID,
			Job: model.JobSummary{
				ID: jobID, Mode: model.JobModeGraph, Status: model.JobStatusStopped,
			},
		})
	}
	handler := &Handler{jobService: observationHandlerJobService{jobs: jobs}, graphService: graphService}

	handler.enrichLegacyGraphObservations(context.Background(), &response)

	if len(graphService.calls) != 24 {
		t.Fatalf("fallback Graph lookups = %d, want bounded 24", len(graphService.calls))
	}
}
