package wechatoutbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

const (
	maxChunkBytes    = 3500
	sendInterval     = 10 * time.Second
	retryBaseDelay   = time.Minute
	maxRetryDelay    = 15 * time.Minute
	idlePollInterval = 30 * time.Second
)

type SenderResolver func() messaging.Replier

type Service struct {
	repo           repository.WeChatOutboxRepo
	senderResolver SenderResolver
	enqueueMu      sync.Mutex
	wake           chan struct{}
	startOnce      sync.Once
	nextSendAt     time.Time
}

func NewService(senderResolver SenderResolver) (*Service, error) {
	repo, err := repository.NewWeChatOutboxRepo()
	if err != nil {
		return nil, fmt.Errorf("init wechat outbox repo failed: %w", err)
	}
	return &Service{
		repo:           repo,
		senderResolver: senderResolver,
		wake:           make(chan struct{}, 1),
	}, nil
}

func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		go s.run(ctx)
	})
}

func (s *Service) Enqueue(ctx context.Context, req *model.WeChatSendMessageRequest, recipients []string) ([]model.WeChatSendResult, error) {
	if req == nil {
		return nil, fmt.Errorf("wechat outbox request is required")
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, fmt.Errorf("content 必填")
	}

	s.enqueueMu.Lock()
	defer s.enqueueMu.Unlock()

	results := make([]model.WeChatSendResult, 0, len(recipients))
	for _, recipient := range recipients {
		taskID := model.NewWeChatOutboxTaskID()
		if req.IdempotencyKey != "" {
			taskID = model.DeterministicWeChatOutboxTaskID(req.IdempotencyKey, recipient)
		}

		task, err := s.repo.Get(ctx, taskID)
		switch {
		case err == nil:
			if task.ToUserID != recipient || task.Content != content {
				return nil, fmt.Errorf("wechat outbox idempotency conflict: task=%s recipient/content differ", taskID)
			}
		case errors.Is(err, os.ErrNotExist):
			now := time.Now()
			task = &model.WeChatOutboxTask{
				ID:             taskID,
				IdempotencyKey: req.IdempotencyKey,
				ToUserID:       recipient,
				Content:        content,
				Status:         model.WeChatOutboxStatusQueued,
				TotalChunks:    len(splitContent(content, maxChunkBytes)),
				CreatedAt:      now,
				UpdatedAt:      now,
			}
			if err := s.repo.Save(ctx, task); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
		results = append(results, resultFromTask(task))
	}
	s.notify()
	return results, nil
}

func (s *Service) GetResult(ctx context.Context, taskID string) (*model.WeChatSendResult, error) {
	task, err := s.repo.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	result := resultFromTask(task)
	return &result, nil
}

func (s *Service) run(ctx context.Context) {
	logger.Infof(ctx, "[wechat/outbox] worker started")
	for {
		task, wait, err := s.nextTask(ctx)
		if err != nil {
			logger.Errorf(ctx, "[wechat/outbox] load queue failed: %v", err)
			if !waitFor(ctx, idlePollInterval, s.wake) {
				return
			}
			continue
		}
		if task == nil {
			if wait <= 0 {
				wait = idlePollInterval
			}
			if !waitFor(ctx, wait, s.wake) {
				return
			}
			continue
		}

		s.processOne(ctx, task)
	}
}

func (s *Service) nextTask(ctx context.Context) (*model.WeChatOutboxTask, time.Duration, error) {
	tasks, err := s.repo.List(ctx)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now()
	nextSendAt := s.nextSendAt
	for _, task := range tasks {
		if task.LastSentAt == nil {
			continue
		}
		candidate := task.LastSentAt.Add(sendInterval)
		if candidate.After(nextSendAt) {
			nextSendAt = candidate
		}
	}
	s.nextSendAt = nextSendAt
	for _, task := range tasks {
		if task.Status == model.WeChatOutboxStatusSent {
			continue
		}
		readyAt := nextSendAt
		if task.NextAttemptAt != nil && task.NextAttemptAt.After(readyAt) {
			readyAt = *task.NextAttemptAt
		}
		if readyAt.After(now) {
			return nil, time.Until(readyAt), nil
		}
		return task, 0, nil
	}
	return nil, idlePollInterval, nil
}

func (s *Service) processOne(ctx context.Context, task *model.WeChatOutboxTask) {
	chunks := splitContent(task.Content, maxChunkBytes)
	task.TotalChunks = len(chunks)
	if task.NextChunk >= len(chunks) {
		now := time.Now()
		task.Status = model.WeChatOutboxStatusSent
		task.SentAt = &now
		task.UpdatedAt = now
		task.LastError = ""
		task.NextAttemptAt = nil
		if err := s.repo.Save(ctx, task); err != nil {
			logger.Errorf(ctx, "[wechat/outbox] finalize task failed: task=%s err=%v", task.ID, err)
		}
		return
	}

	task.Status = model.WeChatOutboxStatusSending
	task.UpdatedAt = time.Now()
	if err := s.repo.Save(ctx, task); err != nil {
		logger.Errorf(ctx, "[wechat/outbox] mark sending failed: task=%s err=%v", task.ID, err)
		return
	}

	var sendErr error
	sender := s.senderResolver()
	if sender == nil {
		sendErr = fmt.Errorf("微信发送器尚未就绪")
	} else {
		sendErr = sender.SendText(ctx, task.ToUserID, chunks[task.NextChunk])
	}
	if sendErr != nil {
		task.Attempt++
		delay := retryDelay(task.Attempt)
		next := time.Now().Add(delay)
		task.Status = model.WeChatOutboxStatusRetrying
		task.LastError = sendErr.Error()
		task.NextAttemptAt = &next
		task.UpdatedAt = time.Now()
		if err := s.repo.Save(ctx, task); err != nil {
			logger.Errorf(ctx, "[wechat/outbox] persist retry failed: task=%s err=%v original=%v", task.ID, err, sendErr)
			return
		}
		logger.Warnf(ctx, "[wechat/outbox] chunk failed, retry scheduled: task=%s chunk=%d/%d attempt=%d delay=%s err=%v",
			task.ID, task.NextChunk+1, len(chunks), task.Attempt, delay, sendErr)
		return
	}

	task.NextChunk++
	task.Attempt = 0
	task.LastError = ""
	task.NextAttemptAt = nil
	now := time.Now()
	task.LastSentAt = &now
	task.UpdatedAt = now
	s.nextSendAt = now.Add(sendInterval)
	if task.NextChunk >= len(chunks) {
		task.Status = model.WeChatOutboxStatusSent
		task.SentAt = &now
	} else {
		task.Status = model.WeChatOutboxStatusQueued
	}
	if err := s.repo.Save(ctx, task); err != nil {
		logger.Errorf(ctx, "[wechat/outbox] persist chunk progress failed: task=%s chunk=%d/%d err=%v",
			task.ID, task.NextChunk, len(chunks), err)
		return
	}
	logger.Infof(ctx, "[wechat/outbox] chunk sent: task=%s chunk=%d/%d to=%s",
		task.ID, task.NextChunk, len(chunks), task.ToUserID)
}

func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func resultFromTask(task *model.WeChatOutboxTask) model.WeChatSendResult {
	return model.WeChatSendResult{
		TaskID:      task.ID,
		ToUserID:    task.ToUserID,
		Status:      task.Status,
		Chunks:      task.NextChunk,
		TotalChunks: task.TotalChunks,
		Error:       task.LastError,
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 4 {
		shift = 4
	}
	delay := retryBaseDelay * time.Duration(1<<shift)
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func waitFor(ctx context.Context, delay time.Duration, wake <-chan struct{}) bool {
	if delay <= 0 {
		delay = time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-wake:
		return true
	}
}

func splitContent(content string, maxBytes int) []string {
	if maxBytes <= 0 || len(content) <= maxBytes {
		return []string{content}
	}
	var chunks []string
	for len(content) > maxBytes {
		splitAt := maxBytes
		for splitAt > 0 && !utf8.RuneStart(content[splitAt]) {
			splitAt--
		}
		if idx := strings.LastIndex(content[:splitAt], "\n"); idx > maxBytes*3/4 {
			splitAt = idx + 1
		}
		if splitAt == 0 {
			_, size := utf8.DecodeRuneInString(content)
			splitAt = size
		}
		chunks = append(chunks, content[:splitAt])
		content = content[splitAt:]
	}
	if content != "" {
		chunks = append(chunks, content)
	}
	return chunks
}
