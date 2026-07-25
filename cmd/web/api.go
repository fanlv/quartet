package main

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/fanlv/quartet/cmd/web/handler"
)

func registerRoutes(s *server.Hertz, h *handler.Handler) {
	// Registered outside the /api/v1 group so it skips agentAuthMiddleware,
	// but kept under /api/v1/* so it rides the frontend's existing /api
	// proxy path without needing a second proxy rule.
	s.GET("/api/v1/health", healthHandler)

	api := s.Group("/api/v1", agentAuthMiddleware())

	// Lightweight token validation: returns 200 if the token is valid without
	// probing ACP agents. Used by the frontend AuthGate to avoid blocking on
	// slow/unreachable agents during boot.
	api.GET("/auth/verify", h.AuthVerify)

	agent := api.Group("/agent")
	agent.GET("/list", h.AgentList)
	// Live subscription / quota info for the Codex / Claude ACP agents,
	// shown on the Home page. Refetched on every agent-type switch.
	agent.GET("/usage", h.AgentUsage)
	// Installed CLI version of a known ACP agent, keyed by its serve command.
	// Backs the composer usage strip for agents without a quota view.
	agent.GET("/version", h.AgentVersion)
	// ACP live-config switch: change model / mode / thought_level and get the
	// refreshed selector lists back. Body carries an optional sessionId (live
	// session switch) or agentType (Home session-less preview).
	agent.POST("/config", h.SetACPConfig)

	api.GET("/list-dir", h.ListDir)
	api.POST("/mkdir", h.MkDir)
	api.POST("/read-file", h.ReadFile)
	api.POST("/write-file", h.WriteFile)
	api.GET("/serve-file", h.ServeFile)
	api.GET("/file-exists", h.FileExists)
	api.GET("/search-files", h.SearchFiles)

	api.POST("/upload-file", h.UploadFile)

	api.GET("/recent-dirs", h.GetRecentDirs)
	api.POST("/recent-dirs", h.AddRecentDir)

	sessions := api.Group("/sessions")
	sessions.GET("/:sessionId/messages", h.GetSessionMessages)

	prompt := api.Group("/prompt")
	prompt.POST("/get", h.GetPrompt)
	prompt.POST("/save", h.SavePrompt)

	config := api.Group("/config")

	// eino tab: model catalog + system prompt owned by the standalone
	// eino-cli binary; these handlers only exec `eino-cli ...` and pass JSON
	// through (secrets never touch quartet storage).
	einoCfg := config.Group("/eino")
	einoModel := einoCfg.Group("/model")
	einoModel.GET("/list", h.GetEinoModelList)
	einoModel.POST("/create", h.CreateEinoModel)
	einoModel.DELETE("/:modelId", h.DeleteEinoModel)
	einoCfg.GET("/system-prompt", h.GetEinoSystemPrompt)
	einoCfg.POST("/system-prompt", h.SaveEinoSystemPrompt)

	settings := config.Group("/settings")
	settings.GET("/get", h.GetSettings)
	settings.POST("/save", h.SaveSettings)

	// WeChat (iLink) routes — scan-to-login, account management, and
	// first-contact approval. See cmd/web/handler/wechat_login_api.go.
	wx := api.Group("/wechat")
	wx.POST("/login/start", h.WeChatLoginStart)
	wx.GET("/login/status", h.WeChatLoginStatus)
	wx.GET("/accounts", h.WeChatAccounts)
	wx.POST("/logout", h.WeChatLogout)
	wx.GET("/pending", h.WeChatPending)
	wx.POST("/pending/dismiss", h.WeChatPendingDismiss)
	wx.POST("/admin/add", h.WeChatAdminAdd)
	wx.POST("/admin/remove", h.WeChatAdminRemove)

	// Log viewer routes.
	logs := api.Group("/logs")
	logs.GET("/list", h.LogsList)
	logs.POST("/clear", h.LogsClear)
	logs.POST("/level", h.LogsSetLevel)
	logs.POST("/frontend", h.LogsFrontendReport)

	// Skill management routes
	skills := api.Group("/skills")
	skills.GET("/list", h.SkillList)
	skills.POST("/add", h.SkillAdd)
	skills.POST("/remove", h.SkillRemove)
	skills.GET("/check", h.SkillCheck)
	skills.POST("/update", h.SkillUpdate)
	skills.GET("/find", h.SkillFind)

	// Template routes
	tmpl := api.Group("/template")
	tmpl.POST("/save", h.SaveTemplate)
	tmpl.PUT("/:templateId", h.UpdateTemplate)
	tmpl.GET("/list", h.ListTemplates)
	tmpl.DELETE("/:templateId", h.DeleteTemplate)

	// Graph workflow config routes (config management only; runtime execution
	// is owned by a separate module). These config routes stay auth-only; the
	// read-only run status/events for a *shared* graph job are exposed under
	// /api/v1/public/* further below (see the pub group).
	graph := api.Group("/graph")
	graph.POST("/workflow", h.CreateGraphWorkflow)
	graph.GET("/workflow/list", h.ListGraphWorkflows)
	graph.POST("/workflow/validate", h.ValidateGraphWorkflow)
	graph.GET("/workflow/:workflowId", h.GetGraphWorkflow)
	graph.PUT("/workflow/:workflowId", h.UpdateGraphWorkflow)
	graph.DELETE("/workflow/:workflowId", h.DeleteGraphWorkflow)
	graph.POST("/run/start", h.StartGraphRun)

	// Job routes
	jobGroup := api.Group("/job")
	jobGroup.POST("/create", h.JobCreate)
	jobGroup.GET("/list", h.JobList)
	jobGroup.GET("/:jobId", h.JobGet)
	jobGroup.DELETE("/:jobId", h.JobDelete)
	jobGroup.PUT("/:jobId/title", h.JobUpdateTitle)
	jobGroup.PUT("/:jobId/pin", h.JobUpdatePin)
	jobGroup.PUT("/:jobId/loop-config", h.JobUpdateLoopConfig)
	jobGroup.POST("/:jobId/start", h.JobStart)
	jobGroup.POST("/:jobId/continue", h.JobContinue)
	jobGroup.POST("/:jobId/message", h.JobMessage)
	jobGroup.POST("/:jobId/stop", h.JobStop)
	jobGroup.GET("/:jobId/events", h.JobEvents)
	jobGroup.GET("/:jobId/graph-run", h.GetJobGraphRunStatus)
	jobGroup.GET("/:jobId/graph-run/events", h.JobGraphRunEvents)
	jobGroup.GET("/:jobId/graph-run/hooks", h.JobGraphRunHooks)
	jobGroup.POST("/:jobId/graph-run/stop", h.StopJobGraphRun)
	jobGroup.POST("/:jobId/graph-run/step-stop", h.StepStopJobGraphRun)
	jobGroup.POST("/:jobId/graph-run/cancel-stop", h.CancelStopJobGraphRun)
	jobGroup.POST("/:jobId/graph-run/resume", h.ResumeJobGraphRun)
	jobGroup.POST("/:jobId/graph-run/continue", h.ContinueJobGraphRun)
	jobGroup.PUT("/:jobId/graph-run/version", h.UpdateJobGraphRunVersion)
	jobGroup.DELETE("/:jobId/graph-run", h.DeleteJobGraphRun)

	// Workspace routes
	wsGroup := api.Group("/workspace")
	wsGroup.POST("/create", h.WorkspaceCreate)
	wsGroup.GET("/list", h.WorkspaceList)
	wsGroup.GET("/default-workdir", h.WorkspaceDefaultWorkdir)
	wsGroup.POST("/regenerate-colors", h.WorkspaceRegenerateColors)
	wsGroup.PUT("/order", h.WorkspaceReorder)
	wsGroup.GET("/:id", h.WorkspaceGet)
	wsGroup.PUT("/:id", h.WorkspaceUpdate)
	wsGroup.PUT("/:id/favorite", h.WorkspaceUpdateFavorite)
	wsGroup.DELETE("/:id", h.WorkspaceDelete)

	// Usage stats routes
	statsGroup := api.Group("/stats")
	statsGroup.GET("/usage", h.StatsUsage)

	// System operation routes.
	systemGroup := api.Group("/system")
	systemGroup.POST("/restart-web", h.SystemRestartWeb)

	// Schedule routes
	schGroup := api.Group("/schedule")
	schGroup.POST("/create", h.ScheduleCreate)
	schGroup.GET("/list", h.ScheduleList)
	schGroup.GET("/:scheduleId", h.ScheduleGet)
	schGroup.PUT("/:scheduleId", h.ScheduleUpdate)
	schGroup.DELETE("/:scheduleId", h.ScheduleDelete)
	schGroup.POST("/:scheduleId/toggle", h.ScheduleToggle)
	schGroup.POST("/:scheduleId/run", h.ScheduleRun)

	// Share/Unshare routes (auth required)
	jobGroup.POST("/:jobId/share", h.JobShare)
	jobGroup.POST("/:jobId/unshare", h.JobUnshare)

	// Public read-only routes (no auth, validated by shareToken)
	pub := s.Group("/api/v1/public", shareTokenMiddleware(h.GetJobService()))
	pub.GET("/agent/list", h.AgentList)
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

	// No-matching-route fallback: serve the front-end static build for non-API
	// paths (with SPA index fallback) and a JSON 404 for unknown /api paths.
	// Registered last so it only catches what the concrete routes above miss.
	registerStaticFallback(s)
}

func healthHandler(ctx context.Context, c *app.RequestContext) {
	// authRequired lets the frontend gate protected requests at boot:
	// when true, the UI knows it must collect a token before firing
	// /workspace/list, /job/list, the SSE stream, etc., instead of
	// triggering a wave of 403s. The endpoint stays unauthenticated so
	// a brand-new client with no token can still discover the policy.
	c.JSON(http.StatusOK, map[string]any{
		"status":       "ok",
		"time":         time.Now().Format(time.RFC3339),
		"buildTime":    buildTime,
		"instanceId":   serverInstanceID,
		"authRequired": handler.IsAuthRequired(),
	})
}
