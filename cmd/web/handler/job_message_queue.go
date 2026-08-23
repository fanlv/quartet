package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) JobMessageQueue(_ context.Context, c *app.RequestContext) {
	queueService, ok := h.jobService.(job.MessageQueueService)
	if !ok {
		httputil.InternalError(c, "message queue service is unavailable")
		return
	}
	snapshot, err := queueService.MessageQueue(c.Param("jobId"))
	if err != nil {
		httputil.MapError(c, err, jobErrMappings)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, map[string]any{"code": 0, "queue": snapshot})
}

func (h *Handler) JobMessageQueueDelete(ctx context.Context, c *app.RequestContext) {
	queueService, ok := h.jobService.(job.MessageQueueService)
	if !ok {
		httputil.InternalError(c, "message queue service is unavailable")
		return
	}
	snapshot, err := queueService.DeleteQueuedMessage(ctx, c.Param("jobId"), c.Param("messageId"))
	if err != nil {
		httputil.MapError(c, err, jobErrMappings)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "queue": snapshot})
}

func (h *Handler) JobMessageQueueContinue(ctx context.Context, c *app.RequestContext) {
	queueService, ok := h.jobService.(job.MessageQueueService)
	if !ok {
		httputil.InternalError(c, "message queue service is unavailable")
		return
	}
	snapshot, err := queueService.ContinueMessageQueue(ctx, c.Param("jobId"))
	if err != nil {
		httputil.MapError(c, err, jobErrMappings)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "queue": snapshot})
}

func (h *Handler) prepareQueuedJobDispatch(ctx context.Context, jobID string, item model.QueuedJobMessage) (job.JobRunner, *job.SendMessageOptions, error) {
	j, ok := h.jobService.Get(jobID)
	if !ok {
		return nil, nil, job.ErrJobNotFound
	}
	req := model.JobMessageRequest{
		Messages: item.Messages, ModelID: item.ModelID, AgentType: item.AgentType,
		SessionID: item.SessionID, ClientMessageID: item.ID, ACPMode: item.ACPMode,
		ACPThoughtLevel: item.ACPThoughtLevel, BypassCommand: item.BypassCommand,
	}
	opts, err := h.prepareJobMessage(j, &req)
	if err != nil {
		return nil, nil, err
	}
	opts.FromMessageQueue = true

	runner := newJobRunner(h, j)
	if item.AgentID != "" && item.AgentRevision != "" {
		binding, found, resolveErr := h.agentCatalog.ResolveBinding(ctx, item.AgentID, item.AgentRevision)
		if resolveErr != nil {
			return nil, nil, resolveErr
		}
		if !found {
			return nil, nil, job.ErrJobNotRunnable
		}
		if err := h.validateInteractiveExecutionAgent(ctx, binding.AgentID, binding.Revision); err != nil {
			return nil, nil, err
		}
		if opts.SessionID != "" {
			session, exists := h.lookupSession(opts.SessionID)
			if !exists {
				return nil, nil, fmt.Errorf("session %s was not found", opts.SessionID)
			}
			sessionAgentID, sessionRevision := session.AgentID, session.AgentRevision
			if sessionAgentID == "" || sessionRevision == "" {
				legacy, legacyFound, legacyErr := h.agentCatalog.ResolveLegacyBinding(ctx, session.Type)
				if legacyErr != nil {
					return nil, nil, legacyErr
				}
				if legacyFound {
					sessionAgentID, sessionRevision = legacy.AgentID, legacy.Revision
				}
			}
			if sessionAgentID != binding.AgentID || sessionRevision != binding.Revision {
				if item.SessionID != "" {
					return nil, nil, fmt.Errorf("session %s does not match queued AgentID %s revision %s", opts.SessionID, binding.AgentID, binding.Revision)
				}
				// The client selected an Agent revision but did not pin a session.
				// A later queue item may have advanced the Job's latest session, so
				// create a new session bound to the originally accepted revision.
				opts.SessionID = ""
			}
		}
		releaseExecution, acquired := h.agentExecutions.acquireExecution(binding.AgentID)
		if !acquired {
			return nil, nil, job.ErrJobNotRunnable
		}
		runner.holdPreparedExecution(releaseExecution)
		opts.AgentBinding = &binding
	}
	runner.prepareAccepted = func(acceptedCtx context.Context, acceptedJobID string) error {
		if opts.SessionID != "" {
			h.maybeUpdateSessionModel(opts.SessionID, req.ModelID)
			h.maybeUpdateSessionACPFields(opts.SessionID, req.ACPMode, req.ACPThoughtLevel)
		}
		pendingTitle, userMessage, needSave, shouldRefine := h.planJobTitleUpdate(j, &req)
		if needSave {
			if err := h.jobService.UpdateTitle(acceptedJobID, pendingTitle); err != nil {
				logger.Errorf(acceptedCtx, "[job.queue] update title failed: jobId=%s err=%v", acceptedJobID, err)
			}
		}
		if shouldRefine {
			h.asyncUpdateJobTitle(acceptedCtx, acceptedJobID, userMessage)
		}
		return nil
	}
	return runner, opts, nil
}
