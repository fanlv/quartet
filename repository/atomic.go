package repository

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
)

// AtomicWriteFile writes data to path using the canonical temp-file + rename
// dance so a concurrent reader never observes a half-written file and a crash
// cannot leave a truncated file behind.
//
// Algorithm:
//  1. mkdir -p the parent directory (matching the previous sandbox behaviour).
//  2. Create a temp file in the same directory (so the final rename is atomic
//     on POSIX — rename(2) across directories may not be).
//  3. Write, fsync the file, and close it.
//  4. Rename the temp file over the destination.
//  5. Best-effort fsync of the parent directory so the rename itself is
//     durable.
//
// The signature matches the standard library's WriteFile to make callers
// interchangeable; `mode` is applied to the final file.
func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return fmt.Errorf("atomic write: empty path")
	}

	sb := fileserver.GetFileManager()
	dir := filepath.Dir(path)
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		return fmt.Errorf("atomic write: create dir %q: %w", dir, err)
	}
	if err := sb.FileWrite(&fsmodel.FileWriteRequest{
		File:    path,
		Content: string(data),
		Mode:    uint32(mode),
		Atomic:  true,
	}); err != nil {
		return fmt.Errorf("atomic write: write %q: %w", path, err)
	}

	return nil
}
