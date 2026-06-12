package handler

import (
	"context"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
)

// defaultBrowseRoot is the root we use when the client asks for a directory
// listing without supplying a path. We pick the first entry in the file
// handlers' allow-list (LOCAL_MEMORY by default) rather than $HOME so a
// misconfigured deployment can't be used to enumerate arbitrary user files.
func defaultBrowseRoot() (string, error) {
	for _, r := range allowedRoots() {
		if r != "" {
			return r, nil
		}
	}
	home, err := fileserver.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home, nil
}

// ListDir returns subdirectories under the given path for browser-based directory picking.
func (h *Handler) ListDir(ctx context.Context, c *app.RequestContext) {
	dirPath := strings.TrimSpace(string(c.Query("path")))

	if dirPath == "" {
		root, err := defaultBrowseRoot()
		if err != nil {
			httputil.InternalError(c, "cannot get default browse dir: "+err.Error())
			return
		}
		dirPath = root
	}

	// Must be absolute path
	if !filepath.IsAbs(dirPath) {
		httputil.BadRequest(c, "path must be absolute")
		return
	}

	// Clean the path to prevent traversal
	dirPath = filepath.Clean(dirPath)

	if !isPathInAllowedRegion(dirPath) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	sb := fileserver.GetFileManager()
	listResult, err := sb.FileList(&fsmodel.FileListRequest{Path: dirPath})
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "ListDir", err)
		return
	}

	showFiles := strings.ToLower(strings.TrimSpace(string(c.Query("showFiles")))) == "true"

	dirs := make([]string, 0)
	type fileInfo struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		ModTime string `json:"modTime"`
	}
	var files []fileInfo

	for _, f := range listResult.Files {
		name := f.Name
		if f.IsDir {
			dirs = append(dirs, name)
		} else if showFiles {
			files = append(files, fileInfo{
				Name:    name,
				Size:    f.Size,
				ModTime: time.Unix(f.ModTimeUnix, 0).Format("2006-01-02T15:04:05Z07:00"),
			})
		}
	}
	sort.Strings(dirs)
	if files == nil {
		files = []fileInfo{}
	}

	parent := filepath.Dir(dirPath)
	if parent == dirPath {
		parent = "" // at root
	}

	result := map[string]any{
		"code":    0,
		"current": dirPath,
		"parent":  parent,
		"dirs":    dirs,
	}
	if showFiles {
		result["files"] = files
	}
	c.JSON(http.StatusOK, result)
}

// MkDir creates a new directory under the given parent path.
func (h *Handler) MkDir(ctx context.Context, c *app.RequestContext) {
	var req struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request")
		return
	}
	req.Parent = strings.TrimSpace(req.Parent)
	req.Name = strings.TrimSpace(req.Name)
	if req.Parent == "" || req.Name == "" {
		httputil.BadRequest(c, "parent and name are required")
		return
	}
	if !filepath.IsAbs(req.Parent) {
		httputil.BadRequest(c, "parent must be absolute path")
		return
	}
	// Prevent path traversal in name
	if strings.Contains(req.Name, "/") || strings.Contains(req.Name, "\\") || req.Name == ".." || req.Name == "." {
		httputil.BadRequest(c, "invalid directory name")
		return
	}

	dirPath := filepath.Join(filepath.Clean(req.Parent), req.Name)
	if !isPathInAllowedRegion(dirPath) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}
	sb := fileserver.GetFileManager()
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: dirPath}); err != nil {
		httputil.InternalErrorLog(ctx, c, "MkDir", err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "path": dirPath})
}

func (h *Handler) FileExists(ctx context.Context, c *app.RequestContext) {
	p := strings.TrimSpace(string(c.Query("path")))
	if p == "" {
		c.JSON(http.StatusOK, map[string]any{"code": 0, "exists": false})
		return
	}
	if !filepath.IsAbs(p) {
		c.JSON(http.StatusOK, map[string]any{"code": 0, "exists": false})
		return
	}
	cleaned := filepath.Clean(p)
	if !isPathInAllowedRegion(cleaned) {
		// Refuse to leak existence of files outside the whitelist. Returning
		// exists:false is intentional — a forbidden signal would let a caller
		// probe the parent-dir whitelist by brute force.
		c.JSON(http.StatusOK, map[string]any{"code": 0, "exists": false})
		return
	}
	sb := fileserver.GetFileManager()
	stat, err := sb.FileStat(&fsmodel.FileStatRequest{Path: cleaned})
	exists := err == nil && stat.Exists && !stat.IsDir
	c.JSON(http.StatusOK, map[string]any{"code": 0, "exists": exists})
}

// SearchFiles searches for files matching a keyword under the given directory.
func (h *Handler) SearchFiles(ctx context.Context, c *app.RequestContext) {
	keyword := strings.TrimSpace(string(c.Query("keyword")))
	dirPath := strings.TrimSpace(string(c.Query("dir")))

	if dirPath == "" {
		root, err := defaultBrowseRoot()
		if err != nil {
			httputil.InternalError(c, "cannot get default browse dir: "+err.Error())
			return
		}
		dirPath = root
	}

	if !filepath.IsAbs(dirPath) {
		httputil.BadRequest(c, "dir must be absolute path")
		return
	}
	dirPath = filepath.Clean(dirPath)

	if !isPathInAllowedRegion(dirPath) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	keywordLower := strings.ToLower(keyword)

	type fileResult struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Dir  string `json:"dir"`
	}

	var results []fileResult
	const maxResults = 20
	const maxDepth = 5

	// Bound the search with an independent deadline so a slow FS or an
	// over-broad keyword can't pin a Hertz worker at the server's idle
	// timeout. 3s is enough for any healthy directory tree the caller can
	// reasonably expect to search under (depth=5, results=20).
	searchCtx, searchCancel := context.WithTimeout(ctx, 3*time.Second)
	defer searchCancel()

	skipDirs := map[string]bool{
		"node_modules": true, ".git": true, ".svn": true, ".hg": true,
		"__pycache__": true, ".idea": true, ".vscode": true, "vendor": true,
		"dist": true, "build": true, ".next": true, "target": true,
	}

	sb := fileserver.GetFileManager()

	// listDirWithTimeout runs FileList on a background goroutine so a
	// stalled filesystem call can't outlive searchCtx's deadline. The
	// FileManager interface is context-unaware, so this is the cheapest
	// way to enforce the 3s bound on the hot path.
	listDirWithTimeout := func(dir string) (*fsmodel.FileListResult, error) {
		type listResp struct {
			res *fsmodel.FileListResult
			err error
		}
		resCh := make(chan listResp, 1)
		go func() {
			r, err := sb.FileList(&fsmodel.FileListRequest{Path: dir})
			resCh <- listResp{res: r, err: err}
		}()
		select {
		case <-searchCtx.Done():
			return nil, searchCtx.Err()
		case r := <-resCh:
			return r.res, r.err
		}
	}

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if searchCtx.Err() != nil || depth > maxDepth || len(results) >= maxResults {
			return
		}
		listResult, err := listDirWithTimeout(dir)
		if err != nil {
			logger.Warnf(ctx, "[file.search] list dir failed: root=%s dir=%s keyword=%q depth=%d results=%d err=%v", dirPath, dir, keyword, depth, len(results), err)
			return
		}
		for _, f := range listResult.Files {
			if len(results) >= maxResults {
				return
			}
			name := f.Name
			if strings.HasPrefix(name, ".") {
				continue
			}
			if f.IsDir {
				if skipDirs[name] {
					continue
				}
				walk(filepath.Join(dir, name), depth+1)
			} else {
				if keyword == "" || strings.Contains(strings.ToLower(name), keywordLower) {
					relDir, _ := filepath.Rel(dirPath, dir)
					if relDir == "." {
						relDir = ""
					}
					results = append(results, fileResult{
						Path: filepath.Join(dir, name),
						Name: name,
						Dir:  relDir,
					})
				}
			}
		}
	}

	walk(dirPath, 0)
	if results == nil {
		results = []fileResult{}
	}

	c.JSON(http.StatusOK, map[string]any{
		"code":  0,
		"files": results,
	})
}

// GetRecentDirs returns the list of recently used directories.
func (h *Handler) GetRecentDirs(ctx context.Context, c *app.RequestContext) {
	rd, err := h.recentDirsRepo.Get()
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "GetRecentDirs", err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "dirs": rd.Dirs})
}

// AddRecentDir adds a directory to the recent list.
func (h *Handler) AddRecentDir(ctx context.Context, c *app.RequestContext) {
	var req struct {
		Dir string `json:"dir"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request")
		return
	}
	req.Dir = strings.TrimSpace(req.Dir)
	if req.Dir == "" {
		httputil.BadRequest(c, "dir is required")
		return
	}
	if !filepath.IsAbs(req.Dir) {
		httputil.BadRequest(c, "dir must be absolute path")
		return
	}
	req.Dir = filepath.Clean(req.Dir)

	if !isPathInAllowedRegion(req.Dir) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	sb := fileserver.GetFileManager()
	stat, err := sb.FileStat(&fsmodel.FileStatRequest{Path: req.Dir})
	if err != nil {
		logger.Errorf(ctx, "[recent_dirs] stat failed: dir=%s err=%v", req.Dir, err)
		httputil.InternalError(c, "failed to stat directory")
		return
	}
	if !stat.Exists || !stat.IsDir {
		httputil.BadRequest(c, "directory does not exist")
		return
	}

	if err := h.recentDirsRepo.Add(ctx, req.Dir); err != nil {
		httputil.InternalErrorLog(ctx, c, "AddRecentDir", err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}
