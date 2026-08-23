package handler

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/sandbox"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

// getPublicJob extracts the validated job from context (set by shareTokenMiddleware).
func getPublicJob(c *app.RequestContext) (*model.Job, bool) {
	v, ok := c.Get("publicJob")
	if !ok {
		return nil, false
	}
	j, ok := v.(*model.Job)
	return j, ok
}

// PublicGetSessionMessages serves session messages for a shared job.
// It validates that the requested sessionId belongs to the shared job.
func (h *Handler) PublicGetSessionMessages(ctx context.Context, c *app.RequestContext) {
	job, ok := getPublicJob(c)
	if !ok {
		c.JSON(http.StatusForbidden, map[string]string{"error": "invalid share context"})
		return
	}

	sessionID := c.Param("sessionId")
	if sessionID == "" {
		httputil.BadRequest(c, "sessionId is required")
		return
	}

	// Verify sessionId belongs to this job (loop/interactive or graph node session)
	if !sessionBelongsToJob(job, sessionID) {
		c.JSON(http.StatusForbidden, map[string]string{"error": "session does not belong to this job"})
		return
	}

	// Delegate to the normal handler
	h.GetSessionMessages(ctx, c)
}

// PublicServeFile serves files for a shared job, restricted to the job's session directory
// and the global uploads directory (for user-uploaded attachments).
func (h *Handler) PublicServeFile(ctx context.Context, c *app.RequestContext) {
	job, ok := getPublicJob(c)
	if !ok {
		c.JSON(http.StatusForbidden, map[string]string{"error": "invalid share context"})
		return
	}

	filePath := strings.TrimSpace(string(c.Query("path")))
	if filePath == "" {
		httputil.BadRequest(c, "path is required")
		return
	}
	if !filepath.IsAbs(filePath) {
		httputil.BadRequest(c, "path must be absolute")
		return
	}
	filePath = filepath.Clean(filePath)

	// Resolve symlinks through the sandbox FileManager so the check runs
	// against the same filesystem view as actual reads.
	sb := sandbox.GetFileManager()
	realPath := filePath
	if r, err := sb.FileEvalSymlinks(&fsmodel.FileEvalSymlinksRequest{Path: filePath}); err == nil {
		realPath = r.ResolvedPath
	}

	// Check allowed directories: job's session dir + global uploads dir.
	allowed := false

	// 1. Job's session directory
	sessDir := typepath.LocalSessionsDirInWorkspaceJob(job.WorkspaceID, job.ID)
	realSessDir := sessDir
	if r, err := sb.FileEvalSymlinks(&fsmodel.FileEvalSymlinksRequest{Path: filepath.Clean(sessDir)}); err == nil {
		realSessDir = r.ResolvedPath
	}
	if realPath == realSessDir || strings.HasPrefix(realPath, realSessDir+string(filepath.Separator)) {
		allowed = true
	}

	// 2. Global uploads directory (user-uploaded attachments)
	if !allowed {
		if uploadsDir, err := typepath.UploadsDir(); err == nil {
			realUploadsDir := uploadsDir
			if r, err2 := sb.FileEvalSymlinks(&fsmodel.FileEvalSymlinksRequest{Path: filepath.Clean(uploadsDir)}); err2 == nil {
				realUploadsDir = r.ResolvedPath
			}
			if realPath == realUploadsDir || strings.HasPrefix(realPath, realUploadsDir+string(filepath.Separator)) {
				allowed = true
			}
		}
	}

	if !allowed {
		logger.Debugf(ctx, "[public-serve-file] access denied: path=%s jobId=%s (not under sessions or uploads dir)", filePath, job.ID)
		c.JSON(http.StatusForbidden, map[string]string{"error": "access denied: path is outside job directory"})
		return
	}

	// Delegate to the normal handler
	h.ServeFile(ctx, c)
}
