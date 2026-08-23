package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/msgextra"
)

const messageQueueLimit = 100

func (s *serviceImpl) SetMessageQueueDispatcher(fn MessageQueueDispatcher) {
	s.messageQueueDispatcherMu.Lock()
	s.messageQueueDispatcher = fn
	s.messageQueueDispatcherMu.Unlock()
	if fn == nil {
		return
	}
	s.reconcileInterruptedMessageQueueItems()
	for _, current := range s.List() {
		if current.Mode == model.JobModeInteractive && len(current.MessageQueue) > 0 && !current.MessageQueuePaused {
			s.requestMessageQueueDispatch(current.ID)
		}
	}
}

func (s *serviceImpl) reconcileInterruptedMessageQueueItems() {
	for _, current := range s.List() {
		if current.Mode != model.JobModeInteractive || len(current.MessageQueue) == 0 || current.MessageQueue[0].State != model.QueuedMessageStateProcessing {
			continue
		}
		lock := s.persistLock(current.ID)
		lock.Lock()
		s.mu.Lock()
		job := s.jobs[current.ID]
		if job == nil || len(job.MessageQueue) == 0 || job.MessageQueue[0].State != model.QueuedMessageStateProcessing {
			s.mu.Unlock()
			lock.Unlock()
			continue
		}
		previous := job.DeepCopy()
		interruptedID := job.MessageQueue[0].ID
		job.MessageQueue = append([]model.QueuedJobMessage(nil), job.MessageQueue[1:]...)
		job.MessageQueueVersion++
		if receipt, ok := job.ClientMessageReceipts[interruptedID]; ok && receipt.State == model.ClientMessageStateProcessing {
			receipt.State = model.ClientMessageStateInterrupted
			receipt.FinishedAt = s.nowMillis()
			job.ClientMessageReceipts[interruptedID] = receipt
		}
		if job.ActiveClientMessageID == interruptedID {
			job.ActiveClientMessageID = ""
		}
		s.mu.Unlock()
		if err := s.saveJobWithRetryUnderPersistLock(context.Background(), job, "message_queue_recover_interrupted"); err != nil {
			s.mu.Lock()
			restoreMessageQueueState(job, previous)
			s.mu.Unlock()
			logger.Errorf(context.Background(), "[job.queue] recover interrupted item failed: jobId=%s messageId=%s err=%v", current.ID, interruptedID, err)
		}
		lock.Unlock()
	}
}

func (s *serviceImpl) SubmitMessage(ctx context.Context, jobID string, message model.QueuedJobMessage) (SubmitMessageResult, error) {
	var result SubmitMessageResult
	if message.ID == "" {
		return result, fmt.Errorf("clientMessageId is required")
	}
	if len(message.Messages) == 0 {
		return result, ErrEmptyMessage
	}
	opts := queuedMessageOptions(message)
	payloadHash, err := clientMessagePayloadHash(opts)
	if err != nil {
		return result, err
	}

	lock := s.persistLock(jobID)
	lock.Lock()
	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		lock.Unlock()
		return result, ErrJobNotFound
	}
	if job.Deleted {
		s.mu.Unlock()
		lock.Unlock()
		return result, ErrJobDeleted
	}
	if job.Mode != model.JobModeInteractive {
		s.mu.Unlock()
		lock.Unlock()
		return result, ErrJobNotRunnable
	}
	if _, exists := job.CommandReceipts[message.ID]; exists {
		s.mu.Unlock()
		lock.Unlock()
		return result, fmt.Errorf("%w: %q was used for a slash command", ErrClientMessageIDConflict, message.ID)
	}
	if receipt, exists := job.ClientMessageReceipts[message.ID]; exists {
		if receipt.PayloadHash != payloadHash {
			s.mu.Unlock()
			lock.Unlock()
			return result, fmt.Errorf("%w: %q", ErrClientMessageIDConflict, message.ID)
		}
		result.Disposition = SubmitMessageDuplicate
		if receipt.State == model.ClientMessageStateDeleted {
			result.Disposition = SubmitMessageDeleted
		}
		result.Receipt = newMessageReceipt(message.ID, receipt)
		result.Queue = messageQueueSnapshotLocked(job)
		s.mu.Unlock()
		lock.Unlock()
		return result, nil
	}
	waitingCount := 0
	for i := range job.MessageQueue {
		if job.MessageQueue[i].State != model.QueuedMessageStateProcessing {
			waitingCount++
		}
	}
	if waitingCount >= messageQueueLimit {
		s.mu.Unlock()
		lock.Unlock()
		return result, ErrMessageQueueFull
	}

	previous := job.DeepCopy()
	now := s.nowMillis()
	message.State = model.QueuedMessageStateQueued
	message.Error = ""
	if message.CreatedAt == 0 {
		message.CreatedAt = now
	}
	message.UpdatedAt = now
	job.MessageQueue = append(job.MessageQueue, message)
	job.MessageQueueVersion++
	if job.ClientMessageReceipts == nil {
		job.ClientMessageReceipts = make(map[string]model.ClientMessageReceipt)
	}
	receipt := model.ClientMessageReceipt{
		State: model.ClientMessageStateQueued, PayloadHash: payloadHash, AcceptedAt: now,
	}
	job.ClientMessageReceipts[message.ID] = receipt
	job.UpdatedAt = time.Now()
	result = SubmitMessageResult{
		Disposition: SubmitMessageQueued,
		Receipt:     newMessageReceipt(message.ID, receipt),
		Queue:       messageQueueSnapshotLocked(job),
	}
	s.mu.Unlock()
	err = s.saveJobWithRetryUnderPersistLock(ctx, job, "message_queue_submit")
	if err != nil {
		s.mu.Lock()
		restoreMessageQueueState(job, previous)
		s.mu.Unlock()
		lock.Unlock()
		return SubmitMessageResult{}, err
	}
	lock.Unlock()

	s.publishMessageQueueChanged(jobID)
	s.requestMessageQueueDispatch(jobID)
	if snapshot, snapshotErr := s.MessageQueue(jobID); snapshotErr == nil {
		result.Queue = snapshot
	}
	if current, found := s.messageReceiptByID(jobID, message.ID); found {
		result.Receipt = current
		switch current.State {
		case model.ClientMessageStateProcessing, model.ClientMessageStateCompleted, model.ClientMessageStateFailed, model.ClientMessageStateStopped, model.ClientMessageStateInterrupted:
			result.Disposition = SubmitMessageStarted
		case model.ClientMessageStateDeleted:
			result.Disposition = SubmitMessageDeleted
		default:
			result.Disposition = SubmitMessageQueued
		}
	}
	return result, nil
}

func (s *serviceImpl) messageReceiptByID(jobID, clientMessageID string) (MessageReceipt, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job := s.jobs[jobID]
	if job == nil {
		return MessageReceipt{}, false
	}
	receipt, ok := job.ClientMessageReceipts[clientMessageID]
	if !ok {
		return MessageReceipt{}, false
	}
	return newMessageReceipt(clientMessageID, receipt), true
}

func (s *serviceImpl) MessageQueue(jobID string) (model.MessageQueueSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return model.MessageQueueSnapshot{}, ErrJobNotFound
	}
	if job.Deleted {
		return model.MessageQueueSnapshot{}, ErrJobDeleted
	}
	return messageQueueSnapshotLocked(job), nil
}

func (s *serviceImpl) DeleteQueuedMessage(ctx context.Context, jobID, clientMessageID string) (model.MessageQueueSnapshot, error) {
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return model.MessageQueueSnapshot{}, ErrJobNotFound
	}
	if job.Deleted {
		s.mu.Unlock()
		return model.MessageQueueSnapshot{}, ErrJobDeleted
	}
	index := -1
	for i := range job.MessageQueue {
		if job.MessageQueue[i].ID == clientMessageID {
			index = i
			break
		}
	}
	if index < 0 {
		receipt, exists := job.ClientMessageReceipts[clientMessageID]
		if exists && receipt.State == model.ClientMessageStateDeleted {
			snapshot := messageQueueSnapshotLocked(job)
			s.mu.Unlock()
			return snapshot, nil
		}
		s.mu.Unlock()
		if exists {
			return model.MessageQueueSnapshot{}, ErrQueuedMessageClaimed
		}
		return model.MessageQueueSnapshot{}, ErrQueuedMessageNotFound
	}
	if job.MessageQueue[index].State == model.QueuedMessageStateProcessing {
		s.mu.Unlock()
		return model.MessageQueueSnapshot{}, ErrQueuedMessageClaimed
	}

	previous := job.DeepCopy()
	item := job.MessageQueue[index]
	nextQueue := make([]model.QueuedJobMessage, 0, len(job.MessageQueue)-1)
	nextQueue = append(nextQueue, job.MessageQueue[:index]...)
	nextQueue = append(nextQueue, job.MessageQueue[index+1:]...)
	job.MessageQueue = nextQueue
	job.MessageQueueVersion++
	receipt := job.ClientMessageReceipts[clientMessageID]
	receipt.State = model.ClientMessageStateDeleted
	receipt.FinishedAt = s.nowMillis()
	job.ClientMessageReceipts[clientMessageID] = receipt
	if len(job.MessageQueue) == 0 && job.Status != model.JobStatusRunning {
		job.MessageQueuePaused = false
		job.MessageQueuePauseReason = ""
	} else if index == 0 && item.State == model.QueuedMessageStateBlocked && job.MessageQueuePauseReason == model.MessageQueuePauseBlocked {
		job.MessageQueuePaused = false
		job.MessageQueuePauseReason = ""
	}
	job.UpdatedAt = time.Now()
	snapshot := messageQueueSnapshotLocked(job)
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(ctx, job, "message_queue_delete"); err != nil {
		s.mu.Lock()
		restoreMessageQueueState(job, previous)
		s.mu.Unlock()
		return model.MessageQueueSnapshot{}, err
	}
	s.publishMessageQueueChanged(jobID)
	if !snapshot.Paused {
		s.requestMessageQueueDispatch(jobID)
	}
	return snapshot, nil
}

func (s *serviceImpl) ContinueMessageQueue(ctx context.Context, jobID string) (model.MessageQueueSnapshot, error) {
	lock := s.persistLock(jobID)
	lock.Lock()
	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		lock.Unlock()
		return model.MessageQueueSnapshot{}, ErrJobNotFound
	}
	if job.Deleted {
		s.mu.Unlock()
		lock.Unlock()
		return model.MessageQueueSnapshot{}, ErrJobDeleted
	}
	firstWaiting := firstWaitingQueueIndex(job.MessageQueue)
	if firstWaiting >= 0 && job.MessageQueue[firstWaiting].State == model.QueuedMessageStateBlocked {
		detail := job.MessageQueue[firstWaiting].Error
		s.mu.Unlock()
		lock.Unlock()
		return model.MessageQueueSnapshot{}, fmt.Errorf("%w: %s", ErrMessageQueueBlocked, detail)
	}
	if !job.MessageQueuePaused {
		snapshot := messageQueueSnapshotLocked(job)
		s.mu.Unlock()
		lock.Unlock()
		s.requestMessageQueueDispatch(jobID)
		return snapshot, nil
	}
	previous := job.DeepCopy()
	job.MessageQueuePaused = false
	job.MessageQueuePauseReason = ""
	job.MessageQueueVersion++
	job.UpdatedAt = time.Now()
	snapshot := messageQueueSnapshotLocked(job)
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(ctx, job, "message_queue_continue"); err != nil {
		s.mu.Lock()
		restoreMessageQueueState(job, previous)
		s.mu.Unlock()
		lock.Unlock()
		return model.MessageQueueSnapshot{}, err
	}
	lock.Unlock()
	s.publishMessageQueueChanged(jobID)
	s.requestMessageQueueDispatch(jobID)
	return snapshot, nil
}

func (s *serviceImpl) PauseMessageQueueForStop(ctx context.Context, jobID string) (bool, error) {
	lock := s.persistLock(jobID)
	lock.Lock()
	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		lock.Unlock()
		return false, ErrJobNotFound
	}
	if job.Deleted {
		s.mu.Unlock()
		lock.Unlock()
		return false, ErrJobDeleted
	}
	// Serialize the stop barrier with SubmitMessage. An idle Job with no
	// accepted work needs no persistent pause; if a submission won this lock
	// first, its running/queued state is visible here and is paused.
	if job.Status != model.JobStatusRunning && len(job.MessageQueue) == 0 {
		s.mu.Unlock()
		lock.Unlock()
		return false, nil
	}
	if job.MessageQueuePaused && job.MessageQueuePauseReason == model.MessageQueuePauseUserStopped {
		s.mu.Unlock()
		lock.Unlock()
		return true, nil
	}
	previous := job.DeepCopy()
	job.MessageQueuePaused = true
	job.MessageQueuePauseReason = model.MessageQueuePauseUserStopped
	job.MessageQueueVersion++
	job.UpdatedAt = time.Now()
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(ctx, job, "message_queue_pause"); err != nil {
		s.mu.Lock()
		restoreMessageQueueState(job, previous)
		s.mu.Unlock()
		lock.Unlock()
		return false, err
	}
	lock.Unlock()
	s.publishMessageQueueChanged(jobID)
	return true, nil
}

func (s *serviceImpl) clearEmptyMessageQueuePause(ctx context.Context, jobID string) {
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()
	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok || !job.MessageQueuePaused || len(job.MessageQueue) > 0 {
		s.mu.Unlock()
		return
	}
	previous := job.DeepCopy()
	job.MessageQueuePaused = false
	job.MessageQueuePauseReason = ""
	job.MessageQueueVersion++
	job.UpdatedAt = time.Now()
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(ctx, job, "message_queue_clear_empty_pause"); err != nil {
		s.mu.Lock()
		restoreMessageQueueState(job, previous)
		s.mu.Unlock()
		logger.Errorf(ctx, "[job.queue] clear empty pause failed: jobId=%s err=%v", jobID, err)
		return
	}
	s.publishMessageQueueChanged(jobID)
}

func (s *serviceImpl) requestMessageQueueDispatch(jobID string) {
	if !s.claimMessageQueueDispatch(jobID) {
		return
	}

	safe.Go(context.Background(), func() {
		if !s.dispatchMessageQueueHead(jobID) {
			s.finishMessageQueueDispatch(jobID)
		}
	})
}

func (s *serviceImpl) claimMessageQueueDispatch(jobID string) bool {
	s.messageQueueDispatchMu.Lock()
	defer s.messageQueueDispatchMu.Unlock()
	if s.stopping {
		return false
	}
	if s.messageQueueDispatching == nil {
		s.messageQueueDispatching = make(map[string]bool)
	}
	if s.messageQueueDispatching[jobID] {
		return false
	}
	s.messageQueueDispatching[jobID] = true
	return true
}

func (s *serviceImpl) finishMessageQueueDispatch(jobID string) {
	s.messageQueueDispatchMu.Lock()
	delete(s.messageQueueDispatching, jobID)
	s.messageQueueDispatchMu.Unlock()

	s.messageQueueDispatcherMu.RLock()
	hasDispatcher := s.messageQueueDispatcher != nil
	s.messageQueueDispatcherMu.RUnlock()
	if !hasDispatcher {
		return
	}
	s.mu.RLock()
	job := s.jobs[jobID]
	firstWaiting := -1
	if job != nil {
		firstWaiting = firstWaitingQueueIndex(job.MessageQueue)
	}
	ready := job != nil && !job.Deleted && job.Mode == model.JobModeInteractive &&
		job.Status != model.JobStatusRunning && !job.MessageQueuePaused && firstWaiting >= 0 &&
		job.MessageQueue[firstWaiting].State == model.QueuedMessageStateQueued && !hasProcessingQueueItem(job.MessageQueue)
	s.mu.RUnlock()
	s.doneMu.Lock()
	_, previousRunStillExiting := s.dones[jobID]
	s.doneMu.Unlock()
	ready = ready && !previousRunStillExiting
	if ready {
		s.requestMessageQueueDispatch(jobID)
	}
}

func (s *serviceImpl) dispatchMessageQueueHead(jobID string) bool {
	s.messageQueueDispatcherMu.RLock()
	dispatcher := s.messageQueueDispatcher
	s.messageQueueDispatcherMu.RUnlock()
	if dispatcher == nil {
		return false
	}

	s.mu.RLock()
	job := s.jobs[jobID]
	firstWaiting := -1
	if job != nil {
		firstWaiting = firstWaitingQueueIndex(job.MessageQueue)
	}
	if job == nil || job.Deleted || job.Mode != model.JobModeInteractive || job.Status == model.JobStatusRunning || job.MessageQueuePaused || firstWaiting < 0 || job.MessageQueue[firstWaiting].State != model.QueuedMessageStateQueued || hasProcessingQueueItem(job.MessageQueue) {
		s.mu.RUnlock()
		return false
	}
	item := job.DeepCopy().MessageQueue[firstWaiting]
	s.mu.RUnlock()
	s.doneMu.Lock()
	_, previousRunStillExiting := s.dones[jobID]
	s.doneMu.Unlock()
	if previousRunStillExiting {
		return false
	}

	runner, opts, err := dispatcher(context.Background(), jobID, item)
	if err != nil {
		s.blockMessageQueueHead(jobID, item.ID, err)
		return false
	}
	result, err := s.SendMessage(context.Background(), jobID, runner, opts)
	if err != nil {
		if errors.Is(err, ErrJobRunning) || errors.Is(err, ErrMessageQueueBlocked) {
			return false
		}
		s.blockMessageQueueHead(jobID, item.ID, err)
		return false
	}
	return result.Started()
}

func (s *serviceImpl) blockMessageQueueHead(jobID, clientMessageID string, cause error) {
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()
	s.mu.Lock()
	job := s.jobs[jobID]
	firstWaiting := -1
	if job != nil {
		firstWaiting = firstWaitingQueueIndex(job.MessageQueue)
	}
	if job == nil || firstWaiting < 0 || job.MessageQueue[firstWaiting].ID != clientMessageID {
		s.mu.Unlock()
		return
	}
	previous := job.DeepCopy()
	job.MessageQueue[firstWaiting].State = model.QueuedMessageStateBlocked
	job.MessageQueue[firstWaiting].Error = cause.Error()
	job.MessageQueue[firstWaiting].UpdatedAt = s.nowMillis()
	job.MessageQueuePaused = true
	job.MessageQueuePauseReason = model.MessageQueuePauseBlocked
	job.MessageQueueVersion++
	if receipt, ok := job.ClientMessageReceipts[clientMessageID]; ok {
		receipt.State = model.ClientMessageStateBlocked
		job.ClientMessageReceipts[clientMessageID] = receipt
	}
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(context.Background(), job, "message_queue_block"); err != nil {
		s.mu.Lock()
		restoreMessageQueueState(job, previous)
		s.mu.Unlock()
		logger.Errorf(context.Background(), "[job.queue] persist blocked item failed: jobId=%s messageId=%s err=%v", jobID, clientMessageID, err)
		s.messageQueueDispatchMu.Lock()
		delete(s.messageQueueDispatching, jobID)
		s.messageQueueDispatchMu.Unlock()
		return
	}
	s.messageQueueDispatchMu.Lock()
	delete(s.messageQueueDispatching, jobID)
	s.messageQueueDispatchMu.Unlock()
	s.publishMessageQueueChanged(jobID)
}

func (s *serviceImpl) completeClaimedQueueItem(ctx context.Context, jobID, clientMessageID string) {
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()
	s.mu.Lock()
	job := s.jobs[jobID]
	if job == nil {
		s.mu.Unlock()
		return
	}
	index := -1
	for i := range job.MessageQueue {
		if job.MessageQueue[i].ID == clientMessageID && job.MessageQueue[i].State == model.QueuedMessageStateProcessing {
			index = i
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		return
	}
	previous := job.DeepCopy()
	nextQueue := make([]model.QueuedJobMessage, 0, len(job.MessageQueue)-1)
	nextQueue = append(nextQueue, job.MessageQueue[:index]...)
	nextQueue = append(nextQueue, job.MessageQueue[index+1:]...)
	job.MessageQueue = nextQueue
	job.MessageQueueVersion++
	job.UpdatedAt = time.Now()
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(ctx, job, "message_queue_complete"); err != nil {
		s.mu.Lock()
		restoreMessageQueueState(job, previous)
		s.mu.Unlock()
		logger.Errorf(ctx, "[job.queue] remove completed item failed: jobId=%s messageId=%s err=%v", jobID, clientMessageID, err)
		return
	}
	s.publishMessageQueueChanged(jobID)
}

func queuedMessageOptions(message model.QueuedJobMessage) *SendMessageOptions {
	messages := make([]*schema.Message, 0, len(message.Messages))
	for _, input := range message.Messages {
		msg := schema.UserMessage(input.AgentContent())
		if len(input.FileAttachments) > 0 {
			msg.Extra = map[string]any{
				msgextra.KeyFileAttachments:     input.FileAttachments,
				msgextra.KeyOriginalUserContent: input.Content,
			}
		}
		messages = append(messages, msg)
	}
	var binding *model.AgentRuntimeBinding
	if message.AgentID != "" && message.AgentRevision != "" {
		binding = &model.AgentRuntimeBinding{AgentID: message.AgentID, Revision: message.AgentRevision}
	}
	return &SendMessageOptions{
		SessionID: message.SessionID, IdempotencySessionID: message.SessionID, ClientMessageID: message.ID,
		Messages: messages, AgentType: message.AgentType, AgentBinding: binding, ModelID: message.ModelID,
		ACPMode: message.ACPMode, ACPThoughtLevel: message.ACPThoughtLevel, FromMessageQueue: true,
	}
}

func firstWaitingQueueIndex(items []model.QueuedJobMessage) int {
	for i := range items {
		if items[i].State != model.QueuedMessageStateProcessing {
			return i
		}
	}
	return -1
}

func hasProcessingQueueItem(items []model.QueuedJobMessage) bool {
	for i := range items {
		if items[i].State == model.QueuedMessageStateProcessing {
			return true
		}
	}
	return false
}

func messageQueueSnapshotLocked(job *model.Job) model.MessageQueueSnapshot {
	copyJob := job.DeepCopy()
	items := make([]model.QueuedJobMessage, 0, len(copyJob.MessageQueue))
	var active *model.QueuedJobMessage
	for i := range copyJob.MessageQueue {
		item := copyJob.MessageQueue[i]
		if item.State == model.QueuedMessageStateProcessing {
			activeItem := item
			active = &activeItem
			continue
		}
		items = append(items, item)
	}
	if items == nil {
		items = []model.QueuedJobMessage{}
	}
	return model.MessageQueueSnapshot{
		JobID: job.ID, Version: job.MessageQueueVersion, Paused: job.MessageQueuePaused, PauseReason: job.MessageQueuePauseReason,
		WillContinue: job.Status == model.JobStatusRunning || (!job.MessageQueuePaused && len(items) > 0), Active: active, Items: items,
	}
}

func restoreMessageQueueState(job, previous *model.Job) {
	job.MessageQueue = previous.MessageQueue
	job.MessageQueueVersion = previous.MessageQueueVersion
	job.MessageQueuePaused = previous.MessageQueuePaused
	job.MessageQueuePauseReason = previous.MessageQueuePauseReason
	job.ClientMessageReceipts = previous.ClientMessageReceipts
	job.ActiveClientMessageID = previous.ActiveClientMessageID
	job.UpdatedAt = previous.UpdatedAt
}

func (s *serviceImpl) publishMessageQueueChanged(jobID string) {
	snapshot, err := s.MessageQueue(jobID)
	if err != nil {
		return
	}
	s.Publish(jobID, &model.CustomEvent{
		BaseEvent: model.BaseEvent{Type: model.EventTypeCustom, JobID: jobID, Timestamp: s.nowMillis()},
		Name:      "message_queue_changed",
		Value:     map[string]any{"jobId": snapshot.JobID, "version": snapshot.Version},
	})
}
