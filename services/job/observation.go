package job

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fanlv/quartet/types/model"
)

var ErrInvalidObservationCursor = errors.New("invalid job observation cursor")

const (
	defaultObservationLimit = 200
	maximumObservationLimit = 500
	observationJournalLimit = 16384
)

type jobObservationFingerprint struct {
	status         model.JobStatus
	mode           model.JobMode
	startedAt      int64
	finishedAt     int64
	graphRunID     string
	lastRunOutcome model.RunOutcome
	graphStatus    string
}

type jobObservationSnapshot struct {
	job            model.JobSummary
	fingerprint    jobObservationFingerprint
	graphSessionID string
}

type jobObservationEvent struct {
	sequence            uint64
	job                 model.JobSummary
	previousStatus      model.JobStatus
	graphStatus         string
	previousGraphStatus string
	graphSessionID      string
	occurredAt          int64
}

type jobObservationCursor struct {
	Epoch    string `json:"e"`
	Sequence uint64 `json:"s"`
}

type jobObservationTracker struct {
	mu           sync.Mutex
	epoch        string
	sequence     uint64
	initialized  bool
	fingerprints map[string]jobObservationFingerprint
	events       []jobObservationEvent
}

// ObserveJobs returns every currently active Job plus a stable page of Jobs
// whose observable summary changed after cursor. It scans the in-memory map
// once, so cost is one local O(total jobs) pass rather than dozens of HTTP
// pages and JSON payloads. Pinned ordering is deliberately irrelevant.
func (s *serviceImpl) ObserveJobs(cursor string, limit int) (model.JobObservationResponse, error) {
	if limit <= 0 {
		limit = defaultObservationLimit
	}
	if limit > maximumObservationLimit {
		limit = maximumObservationLimit
	}

	decodedCursor, hasCursor, err := parseJobObservationCursor(cursor)
	if err != nil {
		return model.JobObservationResponse{}, err
	}

	// Serialize snapshot capture with tracker advancement. Without this outer
	// lock, two clients could capture old/new Job states in order but acquire
	// the tracker lock in reverse order, manufacturing a false reverse change.
	s.observations.mu.Lock()
	defer s.observations.mu.Unlock()

	s.mu.RLock()
	jobs := make([]jobObservationSnapshot, 0, len(s.jobs))
	for _, job := range s.jobs {
		if job.Deleted || job.WorkspaceID == "" {
			continue
		}
		summary := summarize(job)
		// Observation clients never need share credentials. Keep the lightweight
		// lifecycle feed incapable of exposing them even to an authenticated UI.
		summary.ShareToken = ""
		jobs = append(jobs, jobObservationSnapshot{
			job: summary,
			fingerprint: jobObservationFingerprint{
				status:         job.Status,
				mode:           job.Mode,
				startedAt:      job.StartedAt,
				finishedAt:     job.FinishedAt,
				graphRunID:     job.GraphRunID,
				lastRunOutcome: job.LastRunOutcome,
			},
		})
	}
	s.mu.RUnlock()

	// Map iteration is intentionally unordered. A deterministic ID order makes
	// sequence assignment and pagination repeatable for tests and clients.
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].job.ID < jobs[j].job.ID })
	return s.observations.observeLocked(jobs, decodedCursor, hasCursor, limit), nil
}

func (t *jobObservationTracker) observeLocked(
	jobs []jobObservationSnapshot,
	cursor jobObservationCursor,
	hasCursor bool,
	limit int,
) model.JobObservationResponse {
	if !t.initialized {
		t.epoch = newObservationEpoch()
		t.fingerprints = make(map[string]jobObservationFingerprint, len(jobs))
		for _, job := range jobs {
			if _, recorded := t.fingerprints[job.job.ID]; !recorded {
				t.fingerprints[job.job.ID] = job.fingerprint
			}
		}
		t.initialized = true
	}

	activeJobs := make([]model.JobSummary, 0)
	for _, job := range jobs {
		if job.job.Status == model.JobStatusPending || job.job.Status == model.JobStatusRunning {
			activeJobs = append(activeJobs, job.job)
		}
	}
	sort.Slice(activeJobs, func(i, j int) bool {
		if activeJobs[i].UpdatedAt != activeJobs[j].UpdatedAt {
			return activeJobs[i].UpdatedAt > activeJobs[j].UpdatedAt
		}
		return activeJobs[i].ID < activeJobs[j].ID
	})

	if !hasCursor || cursor.Epoch != t.epoch || cursor.Sequence > t.sequence || t.cursorExpired(cursor.Sequence) {
		return model.JobObservationResponse{
			ActiveJobs: activeJobs,
			Cursor:     encodeJobObservationCursor(jobObservationCursor{Epoch: t.epoch, Sequence: t.sequence}),
			Reset:      true,
		}
	}

	start := sort.Search(len(t.events), func(i int) bool {
		return t.events[i].sequence > cursor.Sequence
	})
	end := start + limit
	if end > len(t.events) {
		end = len(t.events)
	}
	changes := make([]model.JobObservationEvent, 0, end-start)
	for _, event := range t.events[start:end] {
		changes = append(changes, model.JobObservationEvent{
			EventID:            fmt.Sprintf("%s:%d", t.epoch, event.sequence),
			Job:                event.job,
			PreviousState:      event.previousStatus,
			GraphStatus:        event.graphStatus,
			PreviousGraphState: event.previousGraphStatus,
			GraphSessionID:     event.graphSessionID,
			OccurredAt:         event.occurredAt,
		})
	}
	nextSequence := t.sequence
	if end > start {
		nextSequence = t.events[end-1].sequence
	}

	return model.JobObservationResponse{
		ActiveJobs: activeJobs,
		Changes:    changes,
		Cursor:     encodeJobObservationCursor(jobObservationCursor{Epoch: t.epoch, Sequence: nextSequence}),
		HasMore:    end < len(t.events),
	}
}

func (t *jobObservationTracker) record(job jobObservationSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.initialized {
		t.epoch = newObservationEpoch()
		t.fingerprints = make(map[string]jobObservationFingerprint)
	}
	previous, exists := t.fingerprints[job.job.ID]
	if exists && previous == job.fingerprint {
		return
	}
	t.sequence++
	occurredAt := job.fingerprint.finishedAt
	if occurredAt <= 0 {
		occurredAt = job.fingerprint.startedAt
	}
	if occurredAt <= 0 {
		occurredAt = job.job.UpdatedAt
	}
	t.events = append(t.events, jobObservationEvent{
		sequence:            t.sequence,
		job:                 job.job,
		previousStatus:      previous.status,
		graphStatus:         job.fingerprint.graphStatus,
		previousGraphStatus: previous.graphStatus,
		graphSessionID:      job.graphSessionID,
		occurredAt:          occurredAt,
	})
	t.fingerprints[job.job.ID] = job.fingerprint
	if excess := len(t.events) - observationJournalLimit; excess > 0 {
		copy(t.events, t.events[excess:])
		t.events = t.events[:observationJournalLimit]
	}
}

func (s *serviceImpl) recordJobObservation(job *model.Job, graphStatus string) {
	s.recordJobObservationWithGraphSession(job, graphStatus, "")
}

func (s *serviceImpl) recordJobObservationWithGraphSession(job *model.Job, graphStatus, graphSessionID string) {
	if job == nil || job.Deleted || job.WorkspaceID == "" {
		return
	}
	if job.Mode == model.JobModeGraph && graphStatus == "" && job.GraphRunID != "" {
		return
	}
	summary := summarize(job)
	summary.ShareToken = ""
	s.observations.record(jobObservationSnapshot{
		job:            summary,
		graphSessionID: graphSessionID,
		fingerprint: jobObservationFingerprint{
			status:         job.Status,
			mode:           job.Mode,
			startedAt:      job.StartedAt,
			finishedAt:     job.FinishedAt,
			graphRunID:     job.GraphRunID,
			lastRunOutcome: job.LastRunOutcome,
			graphStatus:    graphStatus,
		},
	})
}

func (t *jobObservationTracker) cursorExpired(sequence uint64) bool {
	return len(t.events) > 0 && sequence+1 < t.events[0].sequence
}

func encodeJobObservationCursor(cursor jobObservationCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeJobObservationCursor(encoded string) (jobObservationCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return jobObservationCursor{}, fmt.Errorf("%w: %v", ErrInvalidObservationCursor, err)
	}
	var cursor jobObservationCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Epoch == "" {
		return jobObservationCursor{}, fmt.Errorf("%w: malformed payload", ErrInvalidObservationCursor)
	}
	return cursor, nil
}

func parseJobObservationCursor(encoded string) (jobObservationCursor, bool, error) {
	if encoded == "" {
		return jobObservationCursor{}, false, nil
	}
	cursor, err := decodeJobObservationCursor(encoded)
	return cursor, true, err
}

func newObservationEpoch() string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	// crypto/rand failure is exceptionally rare. The tracker only needs a
	// process-local identity, so this deterministic fallback is still safe: an
	// old cursor cannot match the non-empty random epoch from a healthy run.
	return fmt.Sprintf("process-local-%d", time.Now().UnixNano())
}
