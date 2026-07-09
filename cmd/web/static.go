package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/types/consts"
)

const defaultStaticDir = "static"

// staticDir returns the directory served as the web UI root. Default "static"
// (relative to the process working directory, i.e. the repo root), overridable
// via QUARTET_STATIC_DIR.
func staticDir() string {
	if v := strings.TrimSpace(os.Getenv(consts.EnvKeyStaticDir)); v != "" {
		return v
	}
	return defaultStaticDir
}

// registerStaticFallback wires the "no matching route" fallback that serves the
// front-end build. It MUST be called after all /api routes are registered.
//
// Hertz cannot mount a root-path wildcard static handler — it collides with the
// /api/v1/* route tree — so static serving hangs off NoRoute instead. NoRoute
// still runs the global middleware chain (CORS, logger) but NOT the /api
// group's auth middleware, which is exactly right for public UI assets.
//
//   - /api/*  unmatched → JSON 404 (an unknown API endpoint, not a page)
//   - others  → serve the file from the static dir; when the file is absent,
//     fall back to index.html so the SPA (query-param routed) boots on any URL.
func registerStaticFallback(h *server.Hertz) {
	root := staticDir()
	indexPath := filepath.Join(root, "index.html")

	h.NoRoute(func(ctx context.Context, c *app.RequestContext) {
		reqPath := string(c.Path())

		// Unknown API path: never fall through to index.html. Return a JSON 404
		// in the same envelope as the rest of the API so callers get a
		// machine-readable error instead of an HTML page.
		if strings.HasPrefix(reqPath, "/api/") {
			httputil.NotFound(c, "unknown api endpoint: "+reqPath)
			return
		}

		if fp, ok := resolveStaticFile(root, reqPath); ok {
			c.File(fp)
			return
		}

		// SPA fallback: an unknown non-API path serves index.html. If the build
		// is missing, surface the full path so the operator knows to build the
		// front-end (make web) rather than staring at a bare 404.
		if !fileExists(indexPath) {
			httputil.InternalError(c, "web UI not built: missing "+indexPath+" (run `make web` to build the front-end into "+root+")")
			return
		}
		c.File(indexPath)
	})
}

// resolveStaticFile maps a request path to a real regular file under root,
// guarding against path traversal. Returns ("", false) when no such file exists
// (the caller then serves the SPA index).
func resolveStaticFile(root, reqPath string) (string, bool) {
	// Confine to root: cleaning a rooted path collapses any "../" so it can
	// never climb above "/", then we join it under root.
	clean := filepath.Clean("/" + strings.TrimPrefix(reqPath, "/"))
	if clean == "/" {
		return "", false // "/" is the index; let the caller serve index.html
	}
	fp := filepath.Join(root, clean)
	if !fileExists(fp) {
		return "", false
	}
	return fp, true
}
