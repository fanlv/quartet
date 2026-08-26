package job

import (
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/types/model"
)

// SetUsageRecorder wires the optional usage-stats sink. Once set, every
// step finalize point hands a Snapshot to the recorder. Passing nil
// disables recording (used by tests).
func (s *serviceImpl) SetUsageRecorder(r usagestats.Recorder) {
	s.mu.Lock()
	s.usageRecorder = r
	s.mu.Unlock()
}

// recordUsageSnapshot is the single call site for handing per-step usage
// stats to the recorder. It centralises:
//   - the nil-recorder gate (recorder is optional)
//   - duration clamping (sub-millisecond successful steps still bump turn count)
//
// Model attribution is the caller's responsibility — pass the resolved
// per-step / session model id, or empty when unknown. We deliberately do
// NOT fall back to Job.FirstModelID because that field is the JobList
// "first session" denormalisation and would mis-attribute model time when
// the per-step or session model has moved on.
//
// Called from interactive / loop iteration / shell finalize positions.
func (s *serviceImpl) recordUsageSnapshot(job *model.Job, handler *loopEventHandler, modelID string, finishedAtMs, durationMs int64) {
	s.mu.RLock()
	recorder := s.usageRecorder
	s.mu.RUnlock()
	if recorder == nil || handler == nil || handler.usage == nil {
		return
	}
	if durationMs < 0 {
		durationMs = 0
	}
	wsID := ""
	if job != nil {
		wsID = job.WorkspaceID
	}
	handler.finalizeUsageEstimate()
	snap := handler.usage.SnapshotWithEventID(handler.usageEventID, wsID, modelID, finishedAtMs, durationMs)
	recorder.Record(snap)
}
