// Package media owns platform-agnostic IM attachment directories. Media that
// is referenced by a persisted message is stored under Quartet data and is
// never swept. Disposable processing files use a separate runtime cache.
package media

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	deeppath "github.com/fanlv/quartet/types/path"
)

const (
	tempCacheSubdir = "quartet-im-media"

	cacheRetention  = 7 * 24 * time.Hour
	cacheSweepEvery = 12 * time.Hour

	// cacheMaxDepth bounds the recursion in sweepCacheTree.
	// Baseline writers only put files directly under the cache root; this
	// only protects against pathological layouts (symlink loops, external
	// tools) from blowing the stack.
	cacheMaxDepth = 32
)

// PersistentDir returns the directory for media referenced by durable IM,
// user-input, Job or session records.
func PersistentDir() (string, error) {
	return deeppath.PersistentIMMediaDir()
}

// CacheDir returns the disposable directory for processing intermediates.
func CacheDir() (string, error) {
	if dir, err := deeppath.IMMediaCacheDir(); err == nil {
		return dir, nil
	}
	tmpDir, err := fileserver.TempDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(tmpDir, tempCacheSubdir), nil
}

// StartCacheCleanup launches a background sweeper that removes stale IM
// media cache files from the dedicated cache directories. It also runs one
// immediate sweep on startup so leftovers from previous runs do not linger.
func StartCacheCleanup(ctx context.Context) {
	safe.Go(ctx, func() {
		sweepCache(time.Now())

		ticker := time.NewTicker(cacheSweepEvery)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				sweepCache(now)
			}
		}
	})
}

func sweepCache(now time.Time) {
	cutoff := now.Add(-cacheRetention)
	for _, dir := range cacheRoots() {
		if err := sweepCacheDir(dir, cutoff); err != nil {
			logger.Warn("[messaging] media cache cleanup failed: dir=%s err=%v", dir, err)
		}
	}
}

func cacheRoots() []string {
	seen := make(map[string]struct{}, 2)
	roots := make([]string, 0, 2)
	if root, err := deeppath.IMMediaCacheDir(); err == nil {
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	if tmpDir, err := fileserver.TempDir(); err == nil {
		root := filepath.Join(tmpDir, tempCacheSubdir)
		if _, ok := seen[root]; !ok {
			roots = append(roots, root)
		}
	}
	return roots
}

func sweepCacheDir(root string, cutoff time.Time) error {
	if root == "" {
		return nil
	}
	sb := fileserver.GetFileManager()
	exists, err := sb.FileExists(root)
	if err != nil {
		return err
	}
	if exists == nil || !exists.Exists {
		return nil
	}
	return sweepCacheTree(sb, root, cutoff, 0)
}

// sweepCacheTree removes stale files under dir. It does NOT remove
// directories that become empty: the underlying FileDelete may map to
// os.RemoveAll, which in a highly concurrent IM download path can race with
// a new file being written under that directory.
//
// Only uses the fileserver abstraction so cleanup keeps working if storage
// is later virtualized.
func sweepCacheTree(sb fileserver.FileManager, dir string, cutoff time.Time, depth int) error {
	if depth >= cacheMaxDepth {
		logger.Warn("[messaging] media cache sweep depth limit reached: dir=%s", dir)
		return nil
	}
	list, err := sb.FileList(&fsmodel.FileListRequest{Path: dir})
	if err != nil {
		logger.Warn("[messaging] media cache list failed: dir=%s err=%v", dir, err)
		return nil
	}
	if list == nil {
		return nil
	}

	for _, f := range list.Files {
		if f.IsDir {
			_ = sweepCacheTree(sb, f.Path, cutoff, depth+1)
			continue
		}

		mod := time.Unix(f.ModTimeUnix, 0)
		if mod.After(cutoff) {
			continue
		}
		if err := sb.FileDelete(&fsmodel.FileDeleteRequest{Path: f.Path}); err != nil {
			logger.Warn("[messaging] media cache remove failed: path=%s err=%v", f.Path, err)
			continue
		}
		logger.Debug("[messaging] removed stale media cache file: %s", f.Path)
	}
	return nil
}
