package job

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/fanlv/quartet/types/model"
)

// ExecuteCommand serializes a command key's check, synchronous side effect and
// receipt persistence under the same per-job lock used by other job writes.
// The callback is intentionally synchronous: persisting a "processing"
// command receipt before an external side effect would strand that receipt on
// an ordinary command failure, while persisting after an unlocked callback
// would allow concurrent duplicates to execute twice.
func (s *serviceImpl) ExecuteCommand(
	ctx context.Context,
	jobID, clientMessageID, payload string,
	execute func() *model.CommandSystemMessageEvent,
) (*model.CommandSystemMessageEvent, bool, error) {
	if clientMessageID == "" {
		if execute == nil {
			return nil, false, fmt.Errorf("command execute callback is required")
		}
		return execute(), false, nil
	}
	payloadSum := sha256.Sum256([]byte(payload))
	payloadHash := hex.EncodeToString(payloadSum[:])

	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return nil, false, ErrJobNotFound
	}
	if job.Deleted {
		s.mu.Unlock()
		return nil, false, ErrJobDeleted
	}
	if _, exists := job.ClientMessageReceipts[clientMessageID]; exists {
		s.mu.Unlock()
		return nil, false, fmt.Errorf("%w: %q was used for an Agent message", ErrClientMessageIDConflict, clientMessageID)
	}
	if receipt, exists := job.CommandReceipts[clientMessageID]; exists {
		if receipt.PayloadHash != payloadHash {
			s.mu.Unlock()
			return nil, false, fmt.Errorf("%w: %q", ErrClientMessageIDConflict, clientMessageID)
		}
		event := cloneCommandEvent(receipt.Event)
		s.mu.Unlock()
		return event, true, nil
	}
	s.mu.Unlock()

	if execute == nil {
		return nil, false, fmt.Errorf("command execute callback is required")
	}
	event := execute()
	if event == nil {
		return nil, false, fmt.Errorf("command execute callback returned nil event")
	}

	// We still hold the persist shard, so no Job save can overtake this command
	// result. Snapshot, write, then mirror: if persistence fails, no receipt is
	// exposed and the caller receives an error instead of a false durable ack.
	s.mu.Lock()
	current, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return nil, false, ErrJobNotFound
	}
	if current.Deleted {
		s.mu.Unlock()
		return nil, false, ErrJobDeleted
	}
	cp := current.DeepCopy()
	if cp.CommandReceipts == nil {
		cp.CommandReceipts = make(map[string]model.CommandReceipt)
	}
	cp.CommandReceipts[clientMessageID] = model.CommandReceipt{
		PayloadHash: payloadHash,
		Event:       cloneCommandEvent(event),
	}
	s.mu.Unlock()

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return nil, false, fmt.Errorf("get repo for workspace %s: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return nil, false, err
	}

	s.mu.Lock()
	if latest, exists := s.jobs[jobID]; exists && latest == current {
		current.CommandReceipts = cp.CommandReceipts
	}
	s.mu.Unlock()
	return cloneCommandEvent(event), false, nil
}

func cloneCommandEvent(event *model.CommandSystemMessageEvent) *model.CommandSystemMessageEvent {
	if event == nil {
		return nil
	}
	copyEvent := *event
	if event.External != nil {
		copyEvent.External = make(map[string]any, len(event.External))
		for key, value := range event.External {
			copyEvent.External[key] = value
		}
	}
	if event.Action != nil {
		copyAction := *event.Action
		copyEvent.Action = &copyAction
	}
	return &copyEvent
}
