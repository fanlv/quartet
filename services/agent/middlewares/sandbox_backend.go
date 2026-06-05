package middlewares

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk/middlewares/agentsmd"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
	sbmodel "github.com/deep-agent/sandbox/types/model"
	"github.com/fanlv/quartet/pkg/sandbox"
)

type sandboxBackend struct {
	client sandbox.Sandbox

	// roots is the allowlist of absolute directories under which Read /
	// Write / Delete / LsInfo are permitted. Each caller passes its own
	// configured root (workspace dir, session-tasks dir, reduction dir,
	// ...). An empty list means "no boundary" and is only valid for
	// tests or migrations — production callers MUST construct via
	// newSandboxBackend with at least one root.
	roots []string
}

// newSandboxBackend builds a sandboxBackend pinned to the given root
// directories. Roots are resolved to absolute, symlink-free paths once at
// construction; every Read / Write / Delete / LsInfo path is then required
// to fall inside one of them after its own symlink resolution. This stops
// a content-controlled path (e.g. an AGENTS.md @import that references
// "/etc/passwd" or "../../host-secret", or a workspace-internal symlink
// pointing at a host-side directory) from reaching the local filesystem
// helpers, which do no confinement of their own.
func newSandboxBackend(client sandbox.Sandbox, roots ...string) *sandboxBackend {
	cleaned := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			// Fall back to the literal (Cleaned) value so a misconfigured
			// root still rejects writes rather than silently allowing
			// everything. Real callers always pass already-absolute paths
			// derived from the workspace layout, so this branch is
			// defensive only.
			abs = filepath.Clean(r)
		}
		// Resolve symlinks in the root itself so the containment check
		// compares like-with-like. If the workspace dir is symlinked
		// (e.g. /home/user/workspaces -> /var/data/.../workspaces), an
		// unresolved root would make every Read of a path under the
		// real target look like an escape because filepath.Rel would
		// produce a "../"-prefixed answer. EvalSymlinks failure here is
		// non-fatal — the unresolved abs path is still safe to use as a
		// containment anchor.
		if real, rerr := filepath.EvalSymlinks(abs); rerr == nil {
			abs = real
		}
		cleaned = append(cleaned, abs)
	}
	return &sandboxBackend{client: client, roots: cleaned}
}

// sanitizePath normalises a caller-supplied path and enforces the
// configured root allowlist. Empty paths and paths containing NUL bytes
// are always rejected. When roots is non-empty the path must resolve to
// somewhere inside at least one of them; otherwise the call is rejected
// with a path-outside-roots error.
//
// Symlinks are resolved before the containment check: a workspace-internal
// symlink whose target lies outside every configured root is rejected
// even though its lexical form (filepath.Clean / Abs / Rel) would suggest
// it stays in-bounds. For paths that do not yet exist (Write of a new
// file), resolution walks up to the deepest existing ancestor, resolves
// that, and re-appends the non-existent tail — so a write through a
// symlinked parent dir is still caught.
//
// Returns the lexical absolute path on success rather than the resolved
// one, so error messages and downstream local-sandbox calls keep using
// the human-meaningful workspace path. The disk operation will re-follow
// the symlink at the OS layer; we have already proven it lands inside
// an allowed root so that follow is safe.
func (b *sandboxBackend) sanitizePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("path contains NUL")
	}
	cleaned := filepath.Clean(p)
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve abs path failed: %w", err)
	}
	if len(b.roots) == 0 {
		return abs, nil
	}
	real, err := resolveReal(abs)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks failed: %w", err)
	}
	for _, root := range b.roots {
		rel, err := filepath.Rel(root, real)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return abs, nil
	}
	return "", fmt.Errorf("path %q is outside allowed roots", abs)
}

// resolveReal returns the symlink-resolved absolute form of p. When p
// does not exist on disk (typical for Write of a new file under an
// existing parent), the deepest existing ancestor is resolved via
// filepath.EvalSymlinks and the non-existent tail is re-appended. This
// lets a single helper cover both read (target exists) and write (parent
// exists, target does not) confinement checks: if any traversed component
// is a symlink, the resolved chain reflects its real target.
//
// Errors other than "not exist" (ELOOP from a symlink cycle, EACCES from
// missing traverse permission) are propagated so the caller rejects the
// request rather than treating an inaccessible target as in-bounds.
func resolveReal(p string) (string, error) {
	cur := p
	var tail string
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if tail == "" {
				return real, nil
			}
			return filepath.Join(real, tail), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the filesystem root without finding an existing
			// ancestor. Fall back to the lexical path so the caller's
			// containment check still runs; on a sane system this
			// branch only fires on filepath.Dir("/") == "/".
			if tail == "" {
				return cur, nil
			}
			return filepath.Join(cur, tail), nil
		}
		if tail == "" {
			tail = filepath.Base(cur)
		} else {
			tail = filepath.Join(filepath.Base(cur), tail)
		}
		cur = parent
	}
}

func (b *sandboxBackend) LsInfo(ctx context.Context, req *plantask.LsInfoRequest) ([]plantask.FileInfo, error) {
	cleanPath, err := b.sanitizePath(req.Path)
	if err != nil {
		return nil, fmt.Errorf("[LsInfo] %w", err)
	}
	// Read-only listing: do not create directories as a side effect. If the
	// caller lists a path that doesn't exist yet, surface that as an empty
	// result so the agent can decide whether to create it explicitly.
	result, err := b.client.FileList(&sbmodel.FileListRequest{
		Path: cleanPath,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("[LsInfo] file list failed, path: %s, err: %w", cleanPath, err)
	}
	if result == nil {
		return nil, nil
	}

	fileInfos := make([]plantask.FileInfo, 0, len(result.Files))
	for _, f := range result.Files {
		fileInfos = append(fileInfos, plantask.FileInfo{
			Path:       f.Path,
			IsDir:      f.IsDir,
			Size:       f.Size,
			ModifiedAt: time.Unix(f.ModTimeUnix, 0).Format(time.RFC3339),
		})
	}
	return fileInfos, nil
}

func (b *sandboxBackend) Read(ctx context.Context, req *plantask.ReadRequest) (*agentsmd.FileContent, error) {
	cleanPath, err := b.sanitizePath(req.FilePath)
	if err != nil {
		return nil, fmt.Errorf("[Read] %w", err)
	}
	result, err := b.client.FileRead(&sbmodel.FileReadRequest{
		File: cleanPath,
	})
	if err != nil {
		return nil, fmt.Errorf("[Read] file read failed, path: %s, err: %w", cleanPath, err)
	}
	if result == nil {
		return nil, fmt.Errorf("[Read] file read returned nil, path: %s", cleanPath)
	}
	return &agentsmd.FileContent{
		Content: result.Content,
	}, nil
}

func (b *sandboxBackend) Write(ctx context.Context, req *plantask.WriteRequest) error {
	cleanPath, err := b.sanitizePath(req.FilePath)
	if err != nil {
		return fmt.Errorf("[Write] %w", err)
	}
	err = b.client.FileWrite(&sbmodel.FileWriteRequest{
		File:    cleanPath,
		Content: req.Content,
	})
	if err != nil {
		return fmt.Errorf("[Write] file write failed, path: %s, err: %w", cleanPath, err)
	}
	return nil
}

func (b *sandboxBackend) Delete(ctx context.Context, req *plantask.DeleteRequest) error {
	cleanPath, err := b.sanitizePath(req.FilePath)
	if err != nil {
		return fmt.Errorf("[Delete] %w", err)
	}
	err = b.client.FileDelete(&sbmodel.FileDeleteRequest{
		Path: cleanPath,
	})
	if err != nil {
		return fmt.Errorf("[Delete] file delete failed, path: %s, err: %w", cleanPath, err)
	}
	return nil
}
