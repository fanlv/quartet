package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/sandbox"
	typepath "github.com/fanlv/quartet/types/path"
)

const maxReadFileSize = 1 << 20    // 1MB
const maxWriteFileSize = 1 << 20   // 1MB
const maxServeFileSize = 10 << 20  // 10MB
const maxUploadFileSize = 10 << 20 // 10MB
const binaryDetectSampleSize = 512 // bytes sampled from file head for UTF-8 validity check

func (h *Handler) ReadFile(ctx context.Context, c *app.RequestContext) {
	var req struct {
		Path  string `json:"path"`
		JobID string `json:"job_id"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request")
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		httputil.BadRequest(c, "path is required")
		return
	}

	if !filepath.IsAbs(req.Path) {
		// Structured log so a recurring 400 is traceable to the exact path /
		// job that produced it — the HTTP access log only records the status,
		// not which caller sent a relative path. A relative path here is a
		// frontend bug (every read-file caller is expected to resolve to an
		// absolute path first), so surface enough to localise the caller.
		logger.Warnf(ctx, "[read-file] rejected non-absolute path: path=%q jobID=%s referer=%q",
			req.Path, req.JobID, string(c.GetHeader("Referer")))
		httputil.BadRequest(c, "path must be absolute")
		return
	}
	filePath := filepath.Clean(req.Path)
	if !isPathInAllowedRegion(filePath) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	sb := sandbox.GetFileManager()
	stat, err := sb.FileStat(&fsmodel.FileStatRequest{Path: filePath})
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "ReadFile/stat", err)
		return
	}
	if !stat.Exists {
		logger.Debugf(ctx, "[read-file] file not found: path=%q jobID=%s referer=%q",
			filePath, req.JobID, string(c.GetHeader("Referer")))
		httputil.NotFound(c, "file not found")
		return
	}
	if stat.IsDir {
		httputil.BadRequest(c, "path is a directory, not a file")
		return
	}
	// Reject very large files up front so we don't allocate the entire contents
	// in memory just to truncate them afterwards.
	if stat.Size > maxReadFileSize {
		c.JSON(http.StatusOK, map[string]any{
			"code":      0,
			"content":   fmt.Sprintf("[File too large to display inline: %d bytes, max %d. Use the download API or preview ServeFile.]", stat.Size, maxReadFileSize),
			"truncated": true,
			"size":      stat.Size,
			"tooLarge":  true,
		})
		return
	}

	// TOCTOU narrowing: re-run the symlink-aware path check immediately
	// before the read. The first check at the top of the handler can be
	// invalidated by a concurrent rename/symlink swap of an ancestor dir;
	// a second check here cannot close the race entirely (the kernel still
	// re-resolves the path at open time) but it shrinks the attacker window
	// from handler-wide to just the function-call gap.
	if !isPathInAllowedRegion(filePath) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	reader, _, err := sb.FileDownload(filePath)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "FileRead", err)
		return
	}
	defer reader.Close()

	// Enforce maxReadFileSize during the actual read to prevent OOM
	// if the file grows concurrently after the FileStat check above.
	data, err := io.ReadAll(io.LimitReader(reader, maxReadFileSize+1))
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "FileRead/ReadAll", err)
		return
	}
	if len(data) > maxReadFileSize {
		c.JSON(http.StatusOK, map[string]any{
			"code":      0,
			"content":   fmt.Sprintf("[File too large to display inline: >%d bytes. Use the download API or preview ServeFile.]", maxReadFileSize),
			"truncated": true,
			"size":      stat.Size,
			"tooLarge":  true,
		})
		return
	}

	n := len(data)

	sample := data[:min(n, binaryDetectSampleSize)]
	sampleOrigLen := len(sample)
	for len(sample) > 0 && !utf8.Valid(sample) {
		sample = sample[:len(sample)-1]
	}
	if (sampleOrigLen > 0 && len(sample) == 0) || !utf8.Valid(sample) || strings.ContainsRune(string(sample), 0) {
		c.JSON(http.StatusOK, map[string]any{
			"code":    0,
			"content": "[Binary file — cannot display]",
			"binary":  true,
			"size":    stat.Size,
		})
		return
	}

	c.JSON(http.StatusOK, map[string]any{
		"code":      0,
		"content":   string(data),
		"truncated": false,
		"size":      stat.Size,
	})
}

func (h *Handler) WriteFile(ctx context.Context, c *app.RequestContext) {
	// Capture the raw request body BEFORE BindJSON so we can tell the
	// difference between "caller typed U+FFFD on purpose" and "caller fed
	// us raw non-UTF-8 bytes and Go's JSON decoder silently replaced them
	// with U+FFFD". Only the latter is a corruption risk; rejecting every
	// U+FFFD in the decoded string (as we did before) also rejects legit
	// text that happens to contain the replacement character.
	rawBody := append([]byte(nil), c.Request.Body()...)

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Base64  bool   `json:"base64,omitempty"`
		JobID   string `json:"job_id"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request")
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		httputil.BadRequest(c, "path is required")
		return
	}
	if req.Base64 {
		// Bound the decoded size, not the encoded payload.
		if base64.StdEncoding.DecodedLen(len(req.Content)) > maxWriteFileSize {
			httputil.BadRequest(c, "content exceeds 1MB limit")
			return
		}
		// Reject obviously-malformed base64 up front so the caller gets a
		// 400 here instead of a 500 bubbling up from FileWrite. We don't
		// keep the decoded bytes — FileWrite with Base64=true decodes them
		// itself right before the syscall, which is the byte-exact path
		// and avoids an extra UTF-8-unsafe round trip through a Go string.
		if _, err := base64.StdEncoding.DecodeString(req.Content); err != nil {
			httputil.BadRequest(c, "invalid base64 content")
			return
		}
	} else {
		if len(req.Content) > maxWriteFileSize {
			httputil.BadRequest(c, "content exceeds 1MB limit")
			return
		}
		// This endpoint is text-first. If callers need to write binary, they must
		// send Base64=true to avoid silent corruption through JSON strings.
		if strings.ContainsRune(req.Content, 0) {
			httputil.BadRequest(c, "binary content must be written with base64=true")
			return
		}
		// JSON strings are required to be valid UTF-8. If the raw body
		// isn't, Go's decoder silently replaces the invalid sequences with
		// U+FFFD, which would then be written to disk as irreversible
		// corruption. Check the raw bytes instead of the decoded string so
		// a legitimate U+FFFD typed by the user (e.g. writing documentation
		// about the replacement character itself) still goes through.
		if !utf8.Valid(rawBody) {
			httputil.BadRequest(c, "request body is not valid UTF-8; write binary content with base64=true or use upload")
			return
		}
	}

	if !filepath.IsAbs(req.Path) {
		httputil.BadRequest(c, "path must be absolute")
		return
	}
	req.Path = filepath.Clean(req.Path)
	if !isPathInAllowedRegion(req.Path) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	parentDir := filepath.Dir(req.Path)
	sb := sandbox.GetFileManager()
	parentStat, err := sb.FileStat(&fsmodel.FileStatRequest{Path: parentDir})
	if err != nil || !parentStat.Exists || !parentStat.IsDir {
		httputil.BadRequest(c, "parent directory does not exist")
		return
	}

	// TOCTOU narrowing: re-validate right before the write syscall. See
	// the comment on the matching check in ReadFile for context.
	if !isPathInAllowedRegion(req.Path) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	if err := sb.FileWrite(&fsmodel.FileWriteRequest{File: req.Path, Content: req.Content, Base64: req.Base64}); err != nil {
		httputil.InternalErrorLog(ctx, c, "WriteFile", err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}

// ServeFile returns raw file content with proper Content-Type for image preview etc.
func (h *Handler) ServeFile(ctx context.Context, c *app.RequestContext) {
	filePath := strings.TrimSpace(string(c.Query("path")))
	if filePath == "" {
		httputil.BadRequest(c, "path is required")
		return
	}
	if !filepath.IsAbs(filePath) {
		httputil.BadRequest(c, "path must be absolute")
		return
	}
	filePath = filepath.Clean(filePath)

	if !isPathAllowedForServe(filePath) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	sb := sandbox.GetFileManager()
	stat, err := sb.FileStat(&fsmodel.FileStatRequest{Path: filePath})
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "ServeFile/stat", err)
		return
	}
	if !stat.Exists {
		httputil.NotFound(c, "file not found")
		return
	}
	if stat.IsDir {
		httputil.BadRequest(c, "path is a directory")
		return
	}
	if stat.Size > maxServeFileSize {
		httputil.BadRequest(c, "file too large")
		return
	}

	// Browser-native requests such as <img src> carry the same-origin session
	// cookie. If we let the browser interpret the response as HTML, SVG
	// or other active content, an attacker-controlled file under LOCAL_MEMORY
	// (workspace checkouts, uploads) could execute script in our origin and
	// issue authenticated requests. Two defenses:
	//   1. nosniff: refuse MIME type sniffing so the browser cannot upgrade
	//      octet-stream to HTML based on content.
	//   2. inline whitelist: only declare a renderable Content-Type for a
	//      small set of opaque preview formats (images minus SVG, audio,
	//      video, PDF). Everything else is forced to an attachment download
	//      with application/octet-stream.
	contentType, inline := serveFileContentType(filePath)
	c.Response.Header.Set("X-Content-Type-Options", "nosniff")
	if !inline {
		downloadName := strings.TrimSpace(string(c.Query("name")))
		if downloadName == "" {
			downloadName = filepath.Base(filePath)
		} else {
			downloadName = filepath.Base(downloadName)
		}
		c.Response.Header.Set("Content-Disposition",
			buildAttachmentDisposition(downloadName))
	}

	// TOCTOU narrowing: re-validate right before FileDownload opens the
	// file. The download is a streaming body attached to the response, so
	// once we cross this call the bytes will reach the client — this is
	// the last place we can refuse.
	if !isPathAllowedForServe(filePath) {
		c.JSON(http.StatusForbidden, httputil.ErrResponse{Code: -1, Msg: "access denied: path is outside allowed directories"})
		return
	}

	reader, _, err := sb.FileDownload(filePath)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "ServeFile/download", err)
		return
	}
	// NOTE: do not defer reader.Close() here. Hertz reads the stream
	// lazily after this handler returns and calls Close() itself via
	// Response.CloseBodyStream(). Closing early would truncate the
	// response body.
	if stat.Size > math.MaxInt32 {
		_ = reader.Close()
		httputil.BadRequest(c, "file too large")
		return
	}

	c.SetContentType(contentType)
	c.SetStatusCode(http.StatusOK)
	c.Response.SetBodyStream(reader, int(stat.Size))
}

// serveFileInlineMIMEs is the allow-list of MIME types we'll declare verbatim
// on /api/v1/serve-file responses. Everything not in this set is downgraded
// to application/octet-stream + attachment.
//
// Notably excluded: image/svg+xml (SVG can embed <script>), text/html,
// application/xhtml+xml, application/xml, text/xml, application/javascript,
// text/javascript — any of these would let an attacker-controlled file under
// LOCAL_MEMORY run script in the app's origin.
var serveFileInlineMIMEs = map[string]struct{}{
	"image/png":       {},
	"image/jpeg":      {},
	"image/gif":       {},
	"image/webp":      {},
	"image/bmp":       {},
	"image/avif":      {},
	"image/heic":      {},
	"image/heif":      {},
	"audio/mpeg":      {},
	"audio/wav":       {},
	"audio/x-wav":     {},
	"audio/ogg":       {},
	"audio/webm":      {},
	"audio/flac":      {},
	"audio/aac":       {},
	"audio/mp4":       {},
	"video/mp4":       {},
	"video/webm":      {},
	"video/ogg":       {},
	"video/quicktime": {},
	"application/pdf": {},
}

// serveFileContentType resolves the response Content-Type for ServeFile and
// reports whether the content is safe to render inline. Anything outside the
// whitelist is returned as application/octet-stream so the browser offers a
// download instead of interpreting it as active content.
func serveFileContentType(filePath string) (contentType string, inline bool) {
	raw := mime.TypeByExtension(filepath.Ext(filePath))
	// mime.TypeByExtension may return "image/png; charset=utf-8"; strip
	// parameters before matching the whitelist.
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(raw, ";", 2)[0]))
	if _, ok := serveFileInlineMIMEs[base]; ok {
		return raw, true
	}
	return "application/octet-stream", false
}

// sanitizeAttachmentFilename strips characters that would let a crafted
// filename break out of the Content-Disposition quoted-string (CR/LF for
// header injection, double-quote and backslash for the quoted-string
// grammar). Non-ASCII bytes are dropped from this ASCII-only fallback;
// buildAttachmentDisposition adds an RFC 5987 filename*= parameter that
// preserves the original Unicode name for compliant browsers.
func sanitizeAttachmentFilename(name string) string {
	if name == "" {
		return "download"
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r > 0x7e {
			continue
		}
		if r == '"' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "download"
	}
	return out
}

// buildAttachmentDisposition returns a Content-Disposition: attachment
// header value that preserves non-ASCII filenames via RFC 5987
// (filename*=UTF-8”<pct-encoded>) while still providing an ASCII-only
// filename= fallback for clients that don't parse the extended form.
// The ASCII fallback is stripped of CR/LF/quote/backslash so a crafted
// upstream filename cannot inject new headers or break the quoted-string
// grammar.
func buildAttachmentDisposition(name string) string {
	if name == "" {
		name = "download"
	}
	ascii := sanitizeAttachmentFilename(name)
	// If the name is already ASCII-safe and identical to the fallback,
	// skip the RFC 5987 param — it's redundant and some older clients
	// dislike the extra parameter.
	if ascii == name {
		return fmt.Sprintf(`attachment; filename="%s"`, ascii)
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii, rfc5987Encode(name))
}

// rfc5987Encode percent-encodes a UTF-8 string for the value of the
// filename* parameter of Content-Disposition (RFC 5987 §3.2). Only
// attr-char tokens (ALPHA/DIGIT and !#$&+-.^_`|~) pass through; every
// other byte, including the full non-ASCII range, is %HH-encoded.
func rfc5987Encode(s string) string {
	const attrChar = "!#$&+-.^_`|~"
	var b strings.Builder
	b.Grow(len(s) * 3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'A' && c <= 'Z'), (c >= 'a' && c <= 'z'), (c >= '0' && c <= '9'):
			b.WriteByte(c)
		case strings.IndexByte(attrChar, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (h *Handler) UploadFile(ctx context.Context, c *app.RequestContext) {
	uploadsDir, err := typepath.UploadsDir()
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "UploadFile/uploadsDir", err)
		return
	}

	sb := sandbox.GetFileManager()
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: uploadsDir}); err != nil {
		httputil.InternalErrorLog(ctx, c, "UploadFile/mkdir", err)
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		httputil.BadRequest(c, "file is required")
		return
	}

	if file.Size > maxUploadFileSize {
		httputil.BadRequest(c, "file exceeds 10MB limit")
		return
	}

	ext := filepath.Ext(file.Filename)
	// Nanosecond timestamp alone can collide under concurrent uploads on
	// systems with coarse clock resolution; mix in 8 random bytes so the
	// filename is unique even when two uploads land in the same nanosecond.
	var randSuffix [8]byte
	if _, err := rand.Read(randSuffix[:]); err != nil {
		httputil.InternalErrorLog(ctx, c, "UploadFile/rand", err)
		return
	}
	fileName := fmt.Sprintf("%d-%s%s", time.Now().UnixNano(), hex.EncodeToString(randSuffix[:]), ext)
	destPath := filepath.Join(uploadsDir, fileName)

	src, err := file.Open()
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "UploadFile/open", err)
		return
	}
	defer src.Close()

	if _, err := sb.FileUpload(file.Filename, src, destPath); err != nil {
		httputil.InternalErrorLog(ctx, c, "UploadFile/upload", err)
		return
	}

	originalName := filepath.Base(file.Filename)
	contentType := strings.TrimSpace(file.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = mime.TypeByExtension(ext)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.JSON(http.StatusOK, map[string]any{
		"code": 0, "path": destPath, "name": originalName,
		"mimeType": contentType, "size": file.Size,
	})
}

// isPathAllowedForServe checks that the file path is inside the agent's
// allowed filesystem region.
//
// Allowed roots include LOCAL_MEMORY, the uploads dir, every active workspace
// workdir, and the user's home directory. Anything outside that set (system
// files like /etc/shadow, /proc/self/environ, or arbitrary directories on
// other filesystems) is rejected.
func isPathAllowedForServe(filePath string) bool {
	return isPathInAllowedRegion(filePath)
}

// isPathInAllowedRegion is the shared filesystem whitelist check used by the
// file read/write/serve handlers for the non-sandbox path.
func isPathInAllowedRegion(filePath string) bool {
	if filePath == "" {
		return false
	}
	candidates := allowedRoots()
	for _, root := range candidates {
		if root == "" {
			continue
		}
		if hasPathPrefix(filePath, root) {
			return true
		}
	}
	return false
}

// allowedRoots returns the filesystem roots the non-sandbox file handlers are
// allowed to touch. The list is derived from env vars and the workspace
// service, so it stays in sync with the rest of the process. $HOME is always
// included so users can pick directories anywhere under their own home tree.
func allowedRoots() []string {
	var roots []string
	if ws := os.Getenv("LOCAL_MEMORY"); ws != "" {
		roots = append(roots, ws)
	}
	if uploadsDir, err := typepath.UploadsDir(); err == nil {
		roots = append(roots, uploadsDir)
	}
	roots = append(roots, workspaceRoots()...)
	sb := sandbox.GetFileManager()
	if home, err := sb.UserHomeDir(); err == nil && home.Path != "" {
		roots = append(roots, home.Path)
	}
	return roots
}

// hasPathPrefix reports whether filePath is equal to root or is a descendant
// of it, after resolving symlinks where possible. It purposely accepts paths
// that don't yet exist (for write-to-new-file) by falling back to the real
// path of the deepest existing ancestor, so a non-existent leaf cannot be
// used to smuggle a symlinked ancestor past the check.
//
// TOCTOU caveat: hasPathPrefix is a best-effort check. Between this call
// and the actual Read/Write/Serve syscall, a concurrent attacker with
// write access under root could swap a validated directory or ancestor
// for a symlink that escapes the allowlist. A fully race-free guarantee
// requires openat2(RESOLVE_BENEATH) which Go does not expose. The
// deployment model assumes the host filesystem is only writable by trusted
// local users; operators who cannot make that assumption should run the
// server inside a container/chroot that makes the allowlisted roots the
// only reachable tree.
func hasPathPrefix(filePath, root string) bool {
	sb := sandbox.GetFileManager()
	cleanedRoot := filepath.Clean(root)
	realRoot := cleanedRoot
	if r, err := sb.FileEvalSymlinks(&fsmodel.FileEvalSymlinksRequest{Path: cleanedRoot}); err == nil {
		realRoot = r.ResolvedPath
	}

	// If the full path resolves, trust ONLY the real path. Previously this
	// check fell through to a cleaned-prefix match when the real path was
	// outside realRoot, letting a symlink inside the allowed dir point to
	// files outside it.
	if r, err := sb.FileEvalSymlinks(&fsmodel.FileEvalSymlinksRequest{Path: filepath.Clean(filePath)}); err == nil {
		realFile := r.ResolvedPath
		return realFile == realRoot || strings.HasPrefix(realFile, realRoot+string(filepath.Separator))
	}

	// Path doesn't fully resolve (typically the target doesn't exist yet).
	// Walk up to the deepest existing ancestor, resolve its real path, then
	// append the remaining (non-existent) suffix and compare. This still
	// rejects paths whose existing ancestor symlinks outside realRoot.
	cleaned := filepath.Clean(filePath)
	ancestor := cleaned
	var suffix string
	for {
		if r, err := sb.FileEvalSymlinks(&fsmodel.FileEvalSymlinksRequest{Path: ancestor}); err == nil {
			realAncestor := r.ResolvedPath
			effective := realAncestor
			if suffix != "" {
				effective = filepath.Join(realAncestor, suffix)
			}
			return effective == realRoot || strings.HasPrefix(effective, realRoot+string(filepath.Separator))
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return false
		}
		suffix = filepath.Join(filepath.Base(ancestor), suffix)
		ancestor = parent
	}
}
