package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheDirUsesRuntimeCacheSubdir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)

	dir, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	if want := filepath.Join(root, "var", "quartet", "cache", "im-media"); dir != want {
		t.Fatalf("CacheDir = %q, want %q", dir, want)
	}
}

func TestSweepCacheDirRemovesOnlyExpiredFiles(t *testing.T) {
	root := t.TempDir()
	oldFile := filepath.Join(root, "old.png")
	newFile := filepath.Join(root, "new.png")
	nestedDir := filepath.Join(root, "nested")
	nestedOld := filepath.Join(nestedDir, "old.txt")
	for _, file := range []string{oldFile, newFile, nestedOld} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(file), err)
		}
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}

	oldTime := time.Now().Add(-(cacheRetention + time.Hour))
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes oldFile: %v", err)
	}
	if err := os.Chtimes(nestedOld, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes nestedOld: %v", err)
	}

	if err := sweepCacheDir(root, time.Now().Add(-cacheRetention)); err != nil {
		t.Fatalf("sweepCacheDir: %v", err)
	}

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("expected old file removed, stat err=%v", err)
	}
	if _, err := os.Stat(nestedOld); !os.IsNotExist(err) {
		t.Fatalf("expected nested old file removed, stat err=%v", err)
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("expected new file kept, stat err=%v", err)
	}
	if info, err := os.Stat(nestedDir); err != nil {
		t.Fatalf("expected nested dir kept (cleanup no longer removes dirs), stat err=%v", err)
	} else if !info.IsDir() {
		t.Fatalf("expected %s to remain a directory", nestedDir)
	}
}
