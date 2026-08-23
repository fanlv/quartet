package main

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/fanlv/quartet/cmd/web/handler"
	"github.com/fanlv/quartet/services/auth"
)

func registerRoutes(s *server.Hertz, h *handler.Handler) {
	// Registered outside the protected /api/v1 group so clients can discover
	// but kept under /api/v1/* so it rides the frontend's existing /api
	// proxy path without needing a second proxy rule.
	s.GET("/api/v1/health", healthHandler(h))
	s.POST("/api/v1/auth/init", h.AuthInit)
	s.POST("/api/v1/auth/login", h.AuthLogin)

	api := s.Group("/api/v1", sessionAuthMiddleware(h.GetAuthService()))
	permit := func(permission auth.Permission) app.HandlerFunc {
		return permissionMiddleware(h.GetAuthService(), permission)
	}

	api.GET("/auth/me", h.AuthMe)
	api.PUT("/auth/me", h.AuthUpdateProfile)
	api.PUT("/auth/password", h.AuthChangePassword)
	api.POST("/auth/logout", h.AuthLogout)

	users := api.Group("/users")
	users.GET("", permit(auth.PermissionUsersRead), h.UserList)
	users.POST("", permit(auth.PermissionUsersManage), h.UserCreate)
	users.GET("/:userId", permit(auth.PermissionUsersRead), h.UserGet)
	users.PUT("/:userId", permit(auth.PermissionUsersManage), h.UserUpdate)
	users.DELETE("/:userId", permit(auth.PermissionUsersManage), h.UserDelete)
	users.POST("/:userId/reset-password", permit(auth.PermissionUsersManage), h.UserResetPassword)

	api.GET("/permissions", permit(auth.PermissionRolesRead), h.PermissionList)
	roles := api.Group("/roles")
	roles.GET("", permit(auth.PermissionRolesRead), h.RoleList)
	roles.POST("", permit(auth.PermissionRolesManage), h.RoleCreate)
	roles.GET("/:roleId", permit(auth.PermissionRolesRead), h.RoleGet)
	roles.PUT("/:roleId", permit(auth.PermissionRolesManage), h.RoleUpdate)
	roles.DELETE("/:roleId", permit(auth.PermissionRolesManage), h.RoleDelete)

	// Icon proxy: fetches an arbitrary caller-supplied http(s) URL server-side
	// and caches the bytes on disk. That is a server-side request forgery
	// primitive, so it stays behind auth — an unauthenticated version turns the
	// deployment into an open probe for anything the host can reach (LAN
	// services, cloud metadata endpoints). <img> cannot send a header, so the
	// same-origin browser requests carry the session cookie. A shareToken-
	// validated twin lives under /api/v1/public/icon for shared read-only views.
	api.GET("/icon", permit(auth.PermissionAgentRead), h.IconProxy)

	agent := api.Group("/agent")
	agent.GET("/list", permit(auth.PermissionAgentRead), h.AgentList)
	// Complete management catalog: append-only built-ins followed by persisted
	// custom entries, with structured ACP startup definitions and capabilities.
	agent.GET("/catalog", permit(auth.PermissionAgentRead), h.AgentCatalog)
	agent.GET("/catalog/deleted", permit(auth.PermissionAgentRead), h.DeletedAgentCatalog)
	agent.GET("/catalog/:agentId", permit(auth.PermissionAgentRead), h.AgentCatalogDetail)
	// Batch display-info resolution for historical Agent references (old
	// session serve commands, graph node snapshots). Used by the chat view to
	// render agents that were renamed or deleted since the history was made.
	agent.POST("/display-info/resolve", permit(auth.PermissionAgentRead), h.AgentDisplayInfoResolve)
	// Live subscription / quota info for the Codex / Claude ACP agents,
	// shown on the Home page. Refetched on every agent-type switch.
	agent.GET("/usage", permit(auth.PermissionAgentRead), h.AgentUsage)
	// Installed CLI version of a known ACP agent, keyed by its serve command.
	// Backs the composer usage strip for agents without a quota view.
	agent.GET("/version", permit(auth.PermissionAgentRead), h.AgentVersion)
	// Management-page version inventory and controlled built-in upgrade flow.
	// Version checks are read-only and cached; upgrades execute only the preset
	// catalog steps for the route's AgentID.
	agent.GET("/versions", permit(auth.PermissionAgentRead), h.AgentVersionCheck)
	agent.POST("/:agentId/upgrade", permit(auth.PermissionAgentManage), h.AgentUpgrade)
	// ACP live-config switch: change model / mode / thought_level and get the
	// refreshed selector lists back. Body carries an optional sessionId (live
	// session switch) or agentType (Home session-less preview).
	agent.POST("/config", permit(auth.PermissionJobExecute), h.SetACPConfig)
	// Built-in agent installation: candidates are the not-installed,
	// not-deprecated catalog entries; install only accepts an AgentID and
	// executes the catalog's preset flow, then rechecks installation and
	// runs a full ACP validation.
	agent.GET("/install/candidates", permit(auth.PermissionAgentRead), h.AgentInstallCandidates)
	agent.POST("/install", permit(auth.PermissionAgentManage), h.AgentInstall)
	agent.POST("/:agentId/uninstall", permit(auth.PermissionAgentManage), h.AgentUninstall)
	agent.POST("/custom", permit(auth.PermissionAgentManage), h.CreateCustomAgent)
	agent.PUT("/custom/:agentId", permit(auth.PermissionAgentManage), h.UpdateCustomAgent)
	agent.POST("/custom/:agentId/restore", permit(auth.PermissionAgentManage), h.RestoreCustomAgent)
	agent.POST("/:agentId/revalidate", permit(auth.PermissionAgentManage), h.RevalidateAgent)
	agent.GET("/custom/:agentId/delete-impact", permit(auth.PermissionAgentManage), h.CustomAgentDeleteImpact)
	agent.POST("/custom/:agentId/delete", permit(auth.PermissionAgentManage), h.DeleteCustomAgent)

	api.GET("/list-dir", permit(auth.PermissionFileRead), h.ListDir)
	api.POST("/mkdir", permit(auth.PermissionFileWrite), h.MkDir)
	api.POST("/read-file", permit(auth.PermissionFileRead), h.ReadFile)
	api.POST("/write-file", permit(auth.PermissionFileWrite), h.WriteFile)
	api.GET("/serve-file", permit(auth.PermissionFileRead), h.ServeFile)
	api.GET("/file-exists", permit(auth.PermissionFileRead), h.FileExists)
	api.GET("/search-files", permit(auth.PermissionFileRead), h.SearchFiles)
	// Current git branch for a directory (composer workspace tag).
	api.GET("/git-branch", permit(auth.PermissionFileRead), h.GitBranch)

	api.POST("/upload-file", permit(auth.PermissionFileWrite), h.UploadFile)

	api.GET("/recent-dirs", permit(auth.PermissionWorkspaceRead), h.GetRecentDirs)
	api.POST("/recent-dirs", permit(auth.PermissionWorkspaceWrite), h.AddRecentDir)

	sessions := api.Group("/sessions")
	sessions.GET("/:sessionId/messages", permit(auth.PermissionJobRead), h.GetSessionMessages)

	prompt := api.Group("/prompt")
	prompt.POST("/get", permit(auth.PermissionConfigRead), h.GetPrompt)
	prompt.POST("/save", permit(auth.PermissionConfigWrite), h.SavePrompt)

	config := api.Group("/config")

	// eino tab: model catalog + system prompt owned by the standalone
	// eino-cli binary; these handlers only exec `eino-cli ...` and pass JSON
	// through (secrets never touch quartet storage).
	einoCfg := config.Group("/eino")
	einoModel := einoCfg.Group("/model")
	einoModel.GET("/list", permit(auth.PermissionConfigRead), h.GetEinoModelList)
	einoModel.POST("/create", permit(auth.PermissionConfigWrite), h.CreateEinoModel)
	einoModel.DELETE("/:modelId", permit(auth.PermissionConfigWrite), h.DeleteEinoModel)
	einoCfg.GET("/system-prompt", permit(auth.PermissionConfigRead), h.GetEinoSystemPrompt)
	einoCfg.POST("/system-prompt", permit(auth.PermissionConfigWrite), h.SaveEinoSystemPrompt)

	settings := config.Group("/settings")
	settings.GET("/get", permit(auth.PermissionConfigRead), h.GetSettings)
	settings.POST("/save", permit(auth.PermissionConfigWrite), h.SaveSettings)
	settings.GET("/title-generation-agent", permit(auth.PermissionConfigRead), h.GetTitleGenerationAgent)
	settings.PUT("/title-generation-agent", permit(auth.PermissionConfigWrite), h.SaveTitleGenerationAgent)
	settings.GET("/group-reply-agent", permit(auth.PermissionConfigRead), h.GetGroupReplyAgent)
	settings.PUT("/group-reply-agent", permit(auth.PermissionConfigWrite), h.SaveGroupReplyAgent)
	settings.GET("/im-session-agent", permit(auth.PermissionConfigRead), h.GetIMSessionAgent)
	settings.PUT("/im-session-agent", permit(auth.PermissionConfigWrite), h.SaveIMSessionAgent)
	settings.PUT("/agent/:agentId/env", permit(auth.PermissionConfigWrite), h.SaveAgentEnvVars)
	settings.PUT("/agent/:agentId/prefs", permit(auth.PermissionConfigWrite), h.SaveAgentPrefs)

	// WeChat (iLink) routes — scan-to-login, account management, and
	// first-contact approval. See cmd/web/handler/wechat_login_api.go.
	wx := api.Group("/wechat")
	wx.POST("/login/start", permit(auth.PermissionIMManage), h.WeChatLoginStart)
	wx.GET("/login/status", permit(auth.PermissionIMRead), h.WeChatLoginStatus)
	wx.GET("/accounts", permit(auth.PermissionIMRead), h.WeChatAccounts)
	wx.POST("/logout", permit(auth.PermissionIMManage), h.WeChatLogout)
	wx.GET("/pending", permit(auth.PermissionIMRead), h.WeChatPending)
	wx.POST("/pending/dismiss", permit(auth.PermissionIMManage), h.WeChatPendingDismiss)
	wx.POST("/admin/add", permit(auth.PermissionIMManage), h.WeChatAdminAdd)
	wx.POST("/admin/remove", permit(auth.PermissionIMManage), h.WeChatAdminRemove)
	// Proactive push (scheduled jobs / scripts) — not reply-driven.
	wx.GET("/outbox/status", permit(auth.PermissionIMRead), h.WeChatOutboxStatus)
	wx.POST("/send", permit(auth.PermissionIMSend), h.WeChatSend)
	wx.GET("/outbox/:taskId", permit(auth.PermissionIMRead), h.WeChatOutboxGet)

	// Log viewer routes.
	logs := api.Group("/logs")
	logs.GET("/list", permit(auth.PermissionLogsRead), h.LogsList)
	logs.POST("/clear", permit(auth.PermissionLogsManage), h.LogsClear)
	logs.POST("/level", permit(auth.PermissionLogsManage), h.LogsSetLevel)
	logs.POST("/frontend", permit(auth.PermissionLogsReport), h.LogsFrontendReport)

	// Skill management routes
	skills := api.Group("/skills")
	skills.GET("/list", permit(auth.PermissionSkillsRead), h.SkillList)
	skills.POST("/install-project-tools", permit(auth.PermissionSkillsManage), h.SkillInstallProjectTools)
	skills.POST("/add", permit(auth.PermissionSkillsManage), h.SkillAdd)
	skills.POST("/remove", permit(auth.PermissionSkillsManage), h.SkillRemove)
	skills.GET("/check", permit(auth.PermissionSkillsRead), h.SkillCheck)
	skills.POST("/update", permit(auth.PermissionSkillsManage), h.SkillUpdate)
	skills.GET("/find", permit(auth.PermissionSkillsRead), h.SkillFind)

	// Graph workflow config routes (config management only; runtime execution
	// is owned by a separate module). These config routes stay auth-only; the
	// read-only run status/events for a *shared* graph job are exposed under
	// /api/v1/public/* further below (see the pub group).
	graph := api.Group("/graph")
	graph.POST("/workflow", permit(auth.PermissionWorkflowWrite), h.CreateGraphWorkflow)
	graph.GET("/workflow/list", permit(auth.PermissionWorkflowRead), h.ListGraphWorkflows)
	graph.POST("/workflow/validate", permit(auth.PermissionWorkflowRead), h.ValidateGraphWorkflow)
	graph.GET("/workflow/:workflowId", permit(auth.PermissionWorkflowRead), h.GetGraphWorkflow)
	graph.PUT("/workflow/:workflowId", permit(auth.PermissionWorkflowWrite), h.UpdateGraphWorkflow)
	graph.DELETE("/workflow/:workflowId", permit(auth.PermissionWorkflowWrite), h.DeleteGraphWorkflow)
	graph.POST("/run/start", permit(auth.PermissionWorkflowExecute), h.StartGraphRun)

	// Job routes
	jobGroup := api.Group("/job")
	jobGroup.POST("/create", permit(auth.PermissionJobExecute), h.JobCreate)
	jobGroup.GET("/list", permit(auth.PermissionJobRead), h.JobList)
	jobGroup.GET("/observations", permit(auth.PermissionJobRead), h.JobObservations)
	jobGroup.GET("/:jobId", permit(auth.PermissionJobRead), h.JobGet)
	jobGroup.DELETE("/:jobId", permit(auth.PermissionJobManage), h.JobDelete)
	jobGroup.PUT("/:jobId/title", permit(auth.PermissionJobManage), h.JobUpdateTitle)
	jobGroup.PUT("/:jobId/pin", permit(auth.PermissionJobManage), h.JobUpdatePin)
	jobGroup.POST("/:jobId/message", permit(auth.PermissionJobExecute), h.JobMessage)
	jobGroup.POST("/:jobId/stop", permit(auth.PermissionJobExecute), h.JobStop)
	jobGroup.GET("/:jobId/events", permit(auth.PermissionJobRead), h.JobEvents)
	jobGroup.GET("/:jobId/graph-run", permit(auth.PermissionJobRead), h.GetJobGraphRunStatus)
	jobGroup.GET("/:jobId/graph-run/events", permit(auth.PermissionJobRead), h.JobGraphRunEvents)
	jobGroup.GET("/:jobId/graph-run/hooks", permit(auth.PermissionJobRead), h.JobGraphRunHooks)
	jobGroup.POST("/:jobId/graph-run/stop", permit(auth.PermissionJobExecute), h.StopJobGraphRun)
	jobGroup.POST("/:jobId/graph-run/step-stop", permit(auth.PermissionJobExecute), h.StepStopJobGraphRun)
	jobGroup.POST("/:jobId/graph-run/cancel-stop", permit(auth.PermissionJobExecute), h.CancelStopJobGraphRun)
	jobGroup.POST("/:jobId/graph-run/resume", permit(auth.PermissionJobExecute), h.ResumeJobGraphRun)
	jobGroup.POST("/:jobId/graph-run/continue", permit(auth.PermissionJobExecute), h.ContinueJobGraphRun)
	jobGroup.PUT("/:jobId/graph-run/version", permit(auth.PermissionJobExecute), h.UpdateJobGraphRunVersion)
	jobGroup.DELETE("/:jobId/graph-run", permit(auth.PermissionJobManage), h.DeleteJobGraphRun)

	// Workspace routes
	wsGroup := api.Group("/workspace")
	wsGroup.POST("/create", permit(auth.PermissionWorkspaceWrite), h.WorkspaceCreate)
	wsGroup.GET("/list", permit(auth.PermissionWorkspaceRead), h.WorkspaceList)
	wsGroup.GET("/default-workdir", permit(auth.PermissionWorkspaceRead), h.WorkspaceDefaultWorkdir)
	wsGroup.POST("/regenerate-colors", permit(auth.PermissionWorkspaceWrite), h.WorkspaceRegenerateColors)
	wsGroup.PUT("/order", permit(auth.PermissionWorkspaceWrite), h.WorkspaceReorder)
	wsGroup.GET("/:id", permit(auth.PermissionWorkspaceRead), h.WorkspaceGet)
	wsGroup.PATCH("/:id", permit(auth.PermissionWorkspaceWrite), h.WorkspaceUpdate)
	wsGroup.PUT("/:id/favorite", permit(auth.PermissionWorkspaceWrite), h.WorkspaceUpdateFavorite)
	wsGroup.DELETE("/:id", permit(auth.PermissionWorkspaceWrite), h.WorkspaceDelete)

	// Usage stats routes
	statsGroup := api.Group("/stats")
	statsGroup.GET("/usage", permit(auth.PermissionStatsRead), h.StatsUsage)

	// System operation routes.
	systemGroup := api.Group("/system")
	systemGroup.POST("/restart-web", permit(auth.PermissionSystemManage), h.SystemRestartWeb)

	// Schedule routes
	schGroup := api.Group("/schedule")
	schGroup.POST("/create", permit(auth.PermissionScheduleWrite), h.ScheduleCreate)
	schGroup.GET("/list", permit(auth.PermissionScheduleRead), h.ScheduleList)
	schGroup.GET("/:scheduleId", permit(auth.PermissionScheduleRead), h.ScheduleGet)
	schGroup.PUT("/:scheduleId", permit(auth.PermissionScheduleWrite), h.ScheduleUpdate)
	schGroup.DELETE("/:scheduleId", permit(auth.PermissionScheduleWrite), h.ScheduleDelete)
	schGroup.POST("/:scheduleId/toggle", permit(auth.PermissionScheduleWrite), h.ScheduleToggle)
	schGroup.POST("/:scheduleId/run", permit(auth.PermissionScheduleExecute), h.ScheduleRun)

	// Share/Unshare routes (auth required)
	jobGroup.POST("/:jobId/share", permit(auth.PermissionJobShare), h.JobShare)
	jobGroup.POST("/:jobId/unshare", permit(auth.PermissionJobShare), h.JobUnshare)

	// File share routes (auth required)
	fileShare := api.Group("/file-share")
	fileShare.POST("/create", permit(auth.PermissionFileShare), h.FileShareCreate)
	fileShare.POST("/delete", permit(auth.PermissionFileShare), h.FileShareDelete)
	fileShare.GET("/get", permit(auth.PermissionFileShare), h.FileShareGet)

	// Public read-only routes (no auth, validated by shareToken)
	pub := s.Group("/api/v1/public", shareTokenMiddleware(h.GetJobService()))
	pub.GET("/agent/list", h.PublicAgentList)
	pub.GET("/job/:jobId", h.JobGet)
	pub.GET("/job/:jobId/events", h.JobEvents)
	pub.GET("/sessions/:sessionId/messages", h.PublicGetSessionMessages)
	pub.GET("/serve-file", h.PublicServeFile)
	// Graph jobs store their per-node sessions under GraphSessionIDs, not
	// SessionIDs. The shared chat view derives the session sidebar from the
	// run's instances, so a shared graph job needs these two read-only run
	// endpoints to surface any sessions at all. Only the GET status + GET
	// events stream are exposed; every action/version route stays auth-only.
	pub.GET("/job/:jobId/graph-run", h.GetJobGraphRunStatus)
	pub.GET("/job/:jobId/graph-run/events", h.JobGraphRunEvents)
	pub.GET("/job/:jobId/graph-run/hooks", h.JobGraphRunHooks)
	// Agent icons for the shared chat view. PublicAgentList hands back proxied
	// /api/v1/icon URLs, but a shared page holds no agent token, so the
	// frontend rewrites those onto this route and the shareToken carries the
	// authorization instead.
	pub.GET("/icon", h.IconProxy)

	// Public file preview routes (no auth, validated by fileShareToken)
	filePub := s.Group("/api/v1/public/file-preview")
	filePub.GET("/read-file", h.PublicReadFile)
	filePub.GET("/serve-file", h.PublicServeSharedFile)

	// No-matching-route fallback: serve the front-end static build for non-API
	// paths (with SPA index fallback) and a JSON 404 for unknown /api paths.
	// Registered last so it only catches what the concrete routes above miss.
	registerStaticFallback(s)
}

func healthHandler(h *handler.Handler) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// The endpoint stays unauthenticated so clients can select the init, login,
		// recovery, or ready flow without probing any business service.
		state, stateError := h.AuthStatus()
		c.JSON(http.StatusOK, map[string]any{
			"status":     "ok",
			"time":       time.Now().Format(time.RFC3339),
			"buildTime":  buildTime,
			"instanceId": serverInstanceID,
			"authState":  state,
			"authError":  stateError,
		})
	}
}
