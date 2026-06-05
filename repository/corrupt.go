package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
)

// backupCorruptFile renames a file to "<path>.corrupt.<unix-nano>" so that the
// caller's next Save does not overwrite the (recoverable) original contents.
// It is best-effort: errors during the backup are logged, not returned, because
// the calling path is already on a corruption branch.
func backupCorruptFile(ctx context.Context, path string, cause error) {
	if path == "" {
		return
	}
	backup := fmt.Sprintf("%s.corrupt.%d", path, time.Now().UnixNano())
	sb := fileserver.GetFileManager()
	if err := sb.FileMove(&fsmodel.FileMoveRequest{Source: path, Destination: backup}); err != nil {
		logger.Errorf(ctx, "[repo] failed to back up corrupt file %q to %q: %v (original parse error: %v)",
			path, backup, err, cause)
		return
	}
	logger.Errorf(ctx, "[repo] backed up corrupt file %q -> %q (parse error: %v)",
		path, backup, cause)
}

// ensureParentDir is a small helper used by repositories that sometimes skipped
// directory creation in favour of the sandbox doing it implicitly. With the
// real atomic writer, the parent must exist.
func ensureParentDir(path string) error {
	if path == "" {
		return fmt.Errorf("ensureParentDir: empty path")
	}
	sb := fileserver.GetFileManager()
	return sb.MkDir(&fsmodel.MkDirRequest{Path: filepath.Dir(path)})
}
