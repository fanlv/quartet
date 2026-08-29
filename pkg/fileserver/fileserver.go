// Package fileserver is the storage abstraction used by all repository and
// handler code that reads or writes files. It currently wraps a local
// implementation (process-in-memory calls into the upstream SDK's local
// client), but the Storage / FileManager interfaces are the same ones served
// by a remote file server — so a future remote-backed implementation can be
// swapped in without touching callers.
//
// Scope: files + JSONL. Agent runtime tools use their own execution backend.
package fileserver

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdkSandbox "github.com/deep-agent/sandbox/sdk/go"
	localSandbox "github.com/deep-agent/sandbox/sdk/go/local"
	sbmodel "github.com/deep-agent/sandbox/types/model"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
)

// FileManager is the file-only capability surface (read/write/list/delete/
// stat/glob/temp/...). It is Quartet-local; the adapter below translates to
// the current upstream SDK.
type FileManager interface {
	FileRead(req *fsmodel.FileReadRequest) (*fsmodel.FileReadResult, error)
	FileWrite(req *fsmodel.FileWriteRequest) error
	FileList(req *fsmodel.FileListRequest) (*fsmodel.FileListResult, error)
	FileDelete(req *fsmodel.FileDeleteRequest) error
	FileMove(req *fsmodel.FileMoveRequest) error
	FileCopy(req *fsmodel.FileCopyRequest) error
	MkDir(req *fsmodel.MkDirRequest) error
	FileExists(path string) (*fsmodel.FileExistsResult, error)
	FileUpload(filename string, reader io.Reader, destPath string) (*fsmodel.FileUploadResult, error)
	FileDownload(filePath string) (io.ReadCloser, string, error)
	FileCreateTemp(req *fsmodel.FileCreateTempRequest) (*fsmodel.FileCreateTempResult, error)
	FileGlob(req *fsmodel.FileGlobRequest) (*fsmodel.FileGlobResult, error)
	FileEvalSymlinks(req *fsmodel.FileEvalSymlinksRequest) (*fsmodel.FileEvalSymlinksResult, error)
	FileAppend(req *fsmodel.FileAppendRequest) error
	FileStat(req *fsmodel.FileStatRequest) (*fsmodel.FileStatResult, error)
	TempDir() (*fsmodel.TempDirResult, error)
	UserHomeDir() (*fsmodel.UserHomeDirResult, error)
}

// Storage is the full repo-facing storage surface: files + JSONL. Most
// repositories depend on Storage; a handful only need FileManager.
type Storage interface {
	FileManager
	JSONLCountLines(req *fsmodel.JSONLCountRequest) (*fsmodel.JSONLCountResult, error)
	JSONLReadLines(req *fsmodel.JSONLReadRequest) (*fsmodel.JSONLReadResult, error)
	JSONLAppendLine(req *fsmodel.JSONLAppendRequest) error
}

type upstreamStorage interface {
	sdkSandbox.FileManager
	sdkSandbox.JSONLReader
}

type adapter struct {
	up upstreamStorage
}

// localAdapter overrides FileUpload with a direct-to-disk streaming
// implementation. The embedded adapter's upstream is always the in-process
// local client, so writing straight to the destination file (after the same
// path-safety checks) avoids the upstream SDK's io.ReadAll buffer that would
// otherwise pin the whole upload in memory during concurrent uploads.
//
// The streaming path is selected by construction, not by type-asserting the
// upstream at call time: any future remote-backed Storage gets a separate
// adapter (and should implement its own streaming path against its transport)
// rather than falling back to this one and silently re-introducing the buffer.
type localAdapter struct {
	*adapter
}

var (
	localOnce sync.Once
	localInst Storage
)

// GetStorage returns the process-wide local Storage singleton. The local
// client is stateless, so sharing a single instance across goroutines is safe.
func GetStorage() Storage {
	localOnce.Do(func() {
		localInst = NewLocal()
	})
	return localInst
}

// GetFileManager returns the same local singleton, narrowed to FileManager
// for callers that don't need JSONL.
func GetFileManager() FileManager {
	return GetStorage()
}

// NewLocal returns a fresh local Storage. Prefer GetStorage unless you
// specifically need an isolated instance.
func NewLocal() Storage {
	return &localAdapter{adapter: &adapter{up: localSandbox.NewClient()}}
}

// UserHomeDir returns the current user's home directory via the local client.
func UserHomeDir() (string, error) {
	r, err := GetFileManager().UserHomeDir()
	if err != nil {
		return "", err
	}
	return r.Path, nil
}

// TempDir returns the default system temp directory via the local client.
func TempDir() (string, error) {
	r, err := GetFileManager().TempDir()
	if err != nil {
		return "", err
	}
	return r.Path, nil
}

func (a *adapter) FileRead(req *fsmodel.FileReadRequest) (*fsmodel.FileReadResult, error) {
	r, err := a.up.FileRead(&sbmodel.FileReadRequest{File: req.File, Base64: req.Base64})
	if err != nil {
		return nil, err
	}
	return &fsmodel.FileReadResult{Content: r.Content}, nil
}

func (a *adapter) FileWrite(req *fsmodel.FileWriteRequest) error {
	return a.up.FileWrite(&sbmodel.FileWriteRequest{
		File:    req.File,
		Content: req.Content,
		Base64:  req.Base64,
		Mode:    req.Mode,
		Atomic:  req.Atomic,
	})
}

func (a *adapter) FileList(req *fsmodel.FileListRequest) (*fsmodel.FileListResult, error) {
	r, err := a.up.FileList(&sbmodel.FileListRequest{Path: req.Path})
	if err != nil {
		return nil, err
	}
	files := make([]fsmodel.FileInfo, 0, len(r.Files))
	for _, f := range r.Files {
		files = append(files, fsmodel.FileInfo{
			Name:        f.Name,
			Path:        f.Path,
			Size:        f.Size,
			IsDir:       f.IsDir,
			Mode:        f.Mode,
			ModTimeUnix: f.ModTimeUnix,
		})
	}
	return &fsmodel.FileListResult{Files: files}, nil
}

func (a *adapter) FileDelete(req *fsmodel.FileDeleteRequest) error {
	return a.up.FileDelete(&sbmodel.FileDeleteRequest{Path: req.Path})
}

func (a *adapter) FileMove(req *fsmodel.FileMoveRequest) error {
	return a.up.FileMove(&sbmodel.FileMoveRequest{Source: req.Source, Destination: req.Destination})
}

func (a *adapter) FileCopy(req *fsmodel.FileCopyRequest) error {
	return a.up.FileCopy(&sbmodel.FileCopyRequest{Source: req.Source, Destination: req.Destination})
}

func (a *adapter) MkDir(req *fsmodel.MkDirRequest) error {
	return a.up.MkDir(&sbmodel.MkDirRequest{Path: req.Path})
}

func (a *adapter) FileExists(path string) (*fsmodel.FileExistsResult, error) {
	r, err := a.up.FileExists(path)
	if err != nil {
		return nil, err
	}
	return &fsmodel.FileExistsResult{Exists: r.Exists}, nil
}

func (a *adapter) FileUpload(filename string, reader io.Reader, destPath string) (*fsmodel.FileUploadResult, error) {
	// Generic path for non-local adapters: delegate to the upstream SDK.
	// The in-process local client has a streaming override in localAdapter
	// below; any future remote-backed adapter should either provide its
	// own streaming path or live with the SDK's buffering semantics here.
	r, err := a.up.FileUpload(filename, reader, destPath)
	if err != nil {
		return nil, err
	}
	return &fsmodel.FileUploadResult{File: r.File, Size: r.Size}, nil
}

// FileUpload streams the request body straight to disk, bypassing the
// upstream SDK's io.ReadAll so concurrent large uploads don't accumulate
// in memory. Only applicable to localAdapter — the upstream is guaranteed
// to be the in-process local client by construction.
func (a *localAdapter) FileUpload(filename string, reader io.Reader, destPath string) (*fsmodel.FileUploadResult, error) {
	if strings.HasSuffix(destPath, "/") {
		// filepath.Join alone does NOT guarantee the result stays
		// inside destPath — a filename containing "../" would resolve
		// outside. Strip the filename down to its basename before
		// joining so an attacker who controls the multipart filename
		// can't escape the target directory.
		safeName := filepath.Base(filepath.Clean(filename))
		if safeName == "" || safeName == "." || safeName == ".." || safeName == string(filepath.Separator) {
			return nil, fmt.Errorf("invalid upload filename: %q", filename)
		}
		destPath = filepath.Join(destPath, safeName)
	} else {
		// Caller-supplied full destination. Refuse obvious traversal so
		// a bug or a forwarded raw user input can't drop a file into
		// an arbitrary directory. A canonical path survives Clean
		// unchanged; anything else is suspect.
		cleaned := filepath.Clean(destPath)
		if cleaned != destPath || strings.Contains(cleaned, "..") {
			return nil, fmt.Errorf("invalid upload destination: %q", destPath)
		}
	}
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir upload dir: %w", err)
	}
	// Write to a temp file then rename to avoid leaving partially-written
	// files behind if the request aborts.
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("create temp upload file: %w", err)
	}
	tmpName := tmp.Name()
	size, copyErr := io.Copy(tmp, reader)
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("write upload content: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("close upload file: %w", closeErr)
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("finalize upload: %w", err)
	}
	return &fsmodel.FileUploadResult{File: destPath, Size: size}, nil
}

func (a *adapter) FileDownload(filePath string) (io.ReadCloser, string, error) {
	return a.up.FileDownload(filePath)
}

func (a *adapter) FileCreateTemp(req *fsmodel.FileCreateTempRequest) (*fsmodel.FileCreateTempResult, error) {
	r, err := a.up.FileCreateTemp(&sbmodel.FileCreateTempRequest{
		Dir:     req.Dir,
		Pattern: req.Pattern,
		Content: req.Content,
		Base64:  req.Base64,
		Mode:    req.Mode,
	})
	if err != nil {
		return nil, err
	}
	return &fsmodel.FileCreateTempResult{File: r.File}, nil
}

func (a *adapter) FileGlob(req *fsmodel.FileGlobRequest) (*fsmodel.FileGlobResult, error) {
	r, err := a.up.FileGlob(&sbmodel.FileGlobRequest{Path: req.Path, Pattern: req.Pattern, Limit: req.Limit})
	if err != nil {
		return nil, err
	}
	files := append([]string(nil), r.Files...)
	return &fsmodel.FileGlobResult{Files: files, Count: r.Count, Truncated: r.Truncated, Output: r.Output}, nil
}

func (a *adapter) FileEvalSymlinks(req *fsmodel.FileEvalSymlinksRequest) (*fsmodel.FileEvalSymlinksResult, error) {
	r, err := a.up.FileEvalSymlinks(&sbmodel.FileEvalSymlinksRequest{Path: req.Path})
	if err != nil {
		return nil, err
	}
	return &fsmodel.FileEvalSymlinksResult{ResolvedPath: r.ResolvedPath}, nil
}

func (a *adapter) FileAppend(req *fsmodel.FileAppendRequest) error {
	return a.up.FileAppend(&sbmodel.FileAppendRequest{File: req.File, Content: req.Content})
}

func (a *adapter) FileStat(req *fsmodel.FileStatRequest) (*fsmodel.FileStatResult, error) {
	r, err := a.up.FileStat(&sbmodel.FileStatRequest{Path: req.Path})
	if err != nil {
		return nil, err
	}
	return &fsmodel.FileStatResult{
		Exists:      r.Exists,
		IsDir:       r.IsDir,
		Size:        r.Size,
		Mode:        r.Mode,
		ModTimeUnix: r.ModTimeUnix,
	}, nil
}

func (a *adapter) TempDir() (*fsmodel.TempDirResult, error) {
	r, err := a.up.TempDir()
	if err != nil {
		return nil, err
	}
	return &fsmodel.TempDirResult{Path: r.Path}, nil
}

func (a *adapter) UserHomeDir() (*fsmodel.UserHomeDirResult, error) {
	r, err := a.up.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &fsmodel.UserHomeDirResult{Path: r.Path}, nil
}

func (a *adapter) JSONLCountLines(req *fsmodel.JSONLCountRequest) (*fsmodel.JSONLCountResult, error) {
	r, err := a.up.JSONLCountLines(&sbmodel.JSONLCountRequest{File: req.File})
	if err != nil {
		return nil, err
	}
	return &fsmodel.JSONLCountResult{Lines: r.Lines}, nil
}

func (a *adapter) JSONLReadLines(req *fsmodel.JSONLReadRequest) (*fsmodel.JSONLReadResult, error) {
	r, err := a.up.JSONLReadLines(&sbmodel.JSONLReadRequest{File: req.File, StartLine: req.StartLine, Count: req.Count})
	if err != nil {
		return nil, err
	}
	lines := append([]string(nil), r.Lines...)
	return &fsmodel.JSONLReadResult{Lines: lines}, nil
}

func (a *adapter) JSONLAppendLine(req *fsmodel.JSONLAppendRequest) error {
	jsonLines := append([]string(nil), req.JSONString...)
	return a.up.JSONLAppendLine(&sbmodel.JSONLAppendRequest{File: req.File, JSONString: jsonLines})
}
