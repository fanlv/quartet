package repository

import (
	"hash/fnv"
	"os"
	"strings"
	"sync"
)

// validateID rejects empty or traversal-laden IDs so they can never be joined
// into a filesystem path. Shared by every repo that stores one file/dir per
// entity keyed by an externally supplied ID.
func validateID(id string) error {
	if id == "" {
		return os.ErrInvalid
	}
	// NUL terminates C strings on most syscall paths, so a NUL embedded in
	// the ID can truncate the filename after path validation — e.g. "a\x00..".
	if strings.ContainsRune(id, 0) {
		return os.ErrPermission
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return os.ErrPermission
	}
	return nil
}

// lockShard is a fixed-size array of mutexes hashed by ID. Different IDs
// typically map to different shards so independent entities don't serialise
// on each other, while updates to the same ID stay atomic.
type lockShard [64]sync.Mutex

func (s *lockShard) lockFor(id string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	idx := h.Sum32() % uint32(len(s))
	return &s[idx]
}
