package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to path using the canonical temp-file + rename
// dance so a concurrent reader never observes a half-written file and a crash
// cannot leave a truncated file behind.
//
// Algorithm:
//  1. mkdir -p the parent directory.
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

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("atomic write: create dir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("atomic write: create temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	// If anything below fails, remove the temp file so we don't leak it.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomic write: write %q: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomic write: chmod %q: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomic write: fsync %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomic write: close %q: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("atomic write: rename %q: %w", path, err)
	}

	// Best-effort directory fsync so the rename itself is durable.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
