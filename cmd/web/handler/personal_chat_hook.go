package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/shellhook"
	"github.com/fanlv/quartet/pkg/strutil"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) finishPersonalChat(ctx context.Context, task model.PersonalChatTask, session model.PersonalChatSession, run model.PersonalChatRun, reply model.PersonalChatMessage, watched bool) {
	settings, err := h.settingsService.GetSettings()
	if err != nil {
		h.personalChat.RecordHookError(run.ID, err)
		logger.Errorf(ctx, "[personalchat.hook] settings: %v", err)
		return
	}
	if strings.TrimSpace(settings.EndHookScript) == "" || len(task.Queue) > 0 || (watched && settings.EndHookSkipsWatchedJob()) {
		return
	}
	watchedValue := "0"
	if watched {
		watchedValue = "1"
	}
	shellhook.Run(ctx, shellhook.Request{Script: settings.EndHookScript, Workdir: session.Meta.Workdir, Context: map[string]string{
		"QUARTET_HOOK_SOURCE": "interactive", "QUARTET_JOB_TITLE": task.Title, "QUARTET_JOB_ID": task.ID, "QUARTET_JOB_MODE": "personal", "QUARTET_JOB_STATUS": run.State, "QUARTET_RUN_OUTCOME": run.State, "QUARTET_SESSION_ID": session.ID, "QUARTET_LAST_ASSISTANT": strutil.TruncateRunesWithEllipsis(reply.Content, 8000), "QUARTET_ERROR_MESSAGE": run.Error, "QUARTET_JOB_WATCHED": watchedValue,
	}, LogFields: fmt.Sprintf("source=personal task=%s session=%s run=%s", task.ID, session.ID, run.ID), Emit: func(result shellhook.Outcome) {
		if result.Failed {
			h.personalChat.RecordHookError(run.ID, fmt.Errorf("%s\n%s", result.Message, result.Stderr))
			logger.Errorf(ctx, "[personalchat.hook] %s: %s\n%s", run.ID, result.Message, result.Stderr)
		}
	}})
}
