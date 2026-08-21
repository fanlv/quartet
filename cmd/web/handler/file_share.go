package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/sandbox"
	"github.com/fanlv/quartet/repository"
)

func (h *Handler) FileShareCreate(_ context.Context, c *app.RequestContext) {
	var req struct {
		Path string `json:"path"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request")
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		httputil.BadRequest(c, "path is required")
		return
	}
	if !filepath.IsAbs(req.Path) {
		httputil.BadRequest(c, "path must be absolute")
		return
	}
	req.Path = filepath.Clean(req.Path)
	if !isPathInAllowedRegion(req.Path) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	repo := repository.GetFileShareRepo()
	share, err := repo.Create(req.Path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "token": share.Token, "path": share.Path})
}

func (h *Handler) FileShareDelete(_ context.Context, c *app.RequestContext) {
	var req struct {
		Token string `json:"token"`
		Path  string `json:"path"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request")
		return
	}

	repo := repository.GetFileShareRepo()

	token := strings.TrimSpace(req.Token)
	if token == "" {
		path := strings.TrimSpace(req.Path)
		if path == "" {
			httputil.BadRequest(c, "token or path is required")
			return
		}
		share, ok := repo.GetByPath(path)
		if !ok {
			c.JSON(http.StatusOK, map[string]any{"code": 0, "ok": true})
			return
		}
		token = share.Token
	}

	if err := repo.Delete(token); err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "ok": true})
}

func (h *Handler) FileShareGet(_ context.Context, c *app.RequestContext) {
	path := strings.TrimSpace(string(c.Query("path")))
	if path == "" {
		c.JSON(http.StatusOK, map[string]any{"code": 0, "shared": false})
		return
	}
	repo := repository.GetFileShareRepo()
	share, ok := repo.GetByPath(path)
	if !ok {
		c.JSON(http.StatusOK, map[string]any{"code": 0, "shared": false})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "shared": true, "token": share.Token})
}

func (h *Handler) PublicReadFile(_ context.Context, c *app.RequestContext) {
	token := strings.TrimSpace(string(c.Query("fileShareToken")))
	if token == "" {
		c.JSON(http.StatusForbidden, map[string]string{"error": "fileShareToken is required"})
		return
	}

	repo := repository.GetFileShareRepo()
	share, ok := repo.Get(token)
	if !ok {
		c.JSON(http.StatusForbidden, map[string]string{"error": "invalid file share token"})
		return
	}

	c.Set("publicReadFilePath", share.Path)
	h.publicReadFileContent(c, share.Path)
}

func (h *Handler) PublicServeSharedFile(_ context.Context, c *app.RequestContext) {
	token := strings.TrimSpace(string(c.Query("fileShareToken")))
	if token == "" {
		c.JSON(http.StatusForbidden, map[string]string{"error": "fileShareToken is required"})
		return
	}

	repo := repository.GetFileShareRepo()
	share, ok := repo.Get(token)
	if !ok {
		c.JSON(http.StatusForbidden, map[string]string{"error": "invalid file share token"})
		return
	}

	requestedPath := strings.TrimSpace(string(c.Query("path")))
	if requestedPath == "" {
		requestedPath = share.Path
	} else {
		requestedPath = filepath.Clean(requestedPath)
		sharedDir := filepath.Dir(share.Path)
		if !strings.HasPrefix(requestedPath, sharedDir+string(filepath.Separator)) && requestedPath != share.Path {
			c.JSON(http.StatusForbidden, map[string]string{"error": "access denied: path is outside shared directory"})
			return
		}
	}

	c.Request.SetRequestURI("/api/v1/public/file-preview/serve-file?path=" + requestedPath)
	c.Request.URI().QueryArgs().Set("path", requestedPath)
	h.ServeFile(context.Background(), c)
}

func (h *Handler) publicReadFileContent(c *app.RequestContext, filePath string) {
	sb := sandbox.GetFileManager()
	stat, err := sb.FileStat(&fsmodel.FileStatRequest{Path: filePath})
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "stat failed"})
		return
	}
	if !stat.Exists {
		c.JSON(http.StatusNotFound, map[string]string{"error": "file not found"})
		return
	}
	if stat.IsDir {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "path is a directory"})
		return
	}
	if stat.Size > maxReadFileSize {
		c.JSON(http.StatusOK, map[string]any{
			"code":      0,
			"content":   fmt.Sprintf("[File too large to display inline: %d bytes, max %d.]", stat.Size, maxReadFileSize),
			"truncated": true,
			"size":      stat.Size,
			"tooLarge":  true,
		})
		return
	}

	reader, _, err := sb.FileDownload(filePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "read failed"})
		return
	}
	defer reader.Close()

	data, err := io.ReadAll(io.LimitReader(reader, maxReadFileSize+1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "read failed"})
		return
	}
	if len(data) > maxReadFileSize {
		c.JSON(http.StatusOK, map[string]any{
			"code":      0,
			"content":   fmt.Sprintf("[File too large to display inline: >%d bytes.]", maxReadFileSize),
			"truncated": true,
			"size":      stat.Size,
			"tooLarge":  true,
		})
		return
	}

	n := len(data)
	sample := data[:min(n, binaryDetectSampleSize)]
	sampleOrigLen := len(sample)
	for len(sample) > 0 && !utf8.Valid(sample) {
		sample = sample[:len(sample)-1]
	}
	if (sampleOrigLen > 0 && len(sample) == 0) || !utf8.Valid(sample) || strings.ContainsRune(string(sample), 0) {
		c.JSON(http.StatusOK, map[string]any{
			"code":    0,
			"content": "[Binary file — cannot display]",
			"binary":  true,
			"size":    stat.Size,
		})
		return
	}

	c.JSON(http.StatusOK, map[string]any{
		"code":      0,
		"content":   string(data),
		"size":      stat.Size,
		"truncated": false,
	})
}
