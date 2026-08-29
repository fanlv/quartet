package wechat

import (
	"context"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging/wechat/cdn"
	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"

	"github.com/google/uuid"
)

// Size caps for outbound CDN uploads (doc §9.1). These match WeChat's
// client-side limits; exceeding them reliably fails upstream anyway, so we
// fail fast with a clear error instead of burning 60s on a doomed upload.
const (
	maxImageBytes = 10 * 1024 * 1024 // 10 MB
	maxVideoBytes = 25 * 1024 * 1024 // 25 MB
	maxFileBytes  = 25 * 1024 * 1024 // 25 MB
	// maxDownloadBytes bounds downloadFile: it is a safety cap when an
	// outbound ![](url) resolves to an unexpectedly large remote resource.
	// Set just above the largest per-type cap so legitimate payloads pass
	// and runaway ones are rejected before they eat memory.
	maxDownloadBytes = 30 * 1024 * 1024 // 30 MB
)

// supportedAttachmentExts is the outbound-file extension whitelist (doc §9.1).
// Copied from weclaw's messaging/attachment.go supportedAttachmentExts to keep
// parity with upstream. Callers that try to send a non-listed extension get
// an error before any CDN traffic.
var supportedAttachmentExts = []string{
	".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
	".zip", ".txt", ".csv",
	".png", ".jpg", ".jpeg", ".gif", ".webp",
	".mp4", ".mov",
}

func isSupportedAttachmentExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(stripQuery(name)))
	if ext == "" {
		return false
	}
	for _, allowed := range supportedAttachmentExts {
		if ext == allowed {
			return true
		}
	}
	return false
}

// checkAttachmentSize enforces the per-type size caps. itemType is one of
// ilink.ItemTypeImage / ItemTypeVideo / ItemTypeFile.
func checkAttachmentSize(itemType, size int) error {
	var limit int
	var label string
	switch itemType {
	case ilink.ItemTypeImage:
		limit, label = maxImageBytes, "image"
	case ilink.ItemTypeVideo:
		limit, label = maxVideoBytes, "video"
	default:
		limit, label = maxFileBytes, "file"
	}
	if size > limit {
		return fmt.Errorf("wechat/media: %s size %d bytes exceeds limit %d bytes", label, size, limit)
	}
	return nil
}

// newClientID generates a unique client ID used to correlate outgoing
// messages with their acknowledgements.
func newClientID() string {
	return uuid.New().String()
}

// sendMediaFromURL downloads a file from a URL and sends it as a media
// message via the iLink CDN path.
func sendMediaFromURL(ctx context.Context, client *ilink.Client, toUserID, mediaURL, contextToken string) error {
	if !isSupportedAttachmentExt(mediaURL) {
		return fmt.Errorf("wechat/media: unsupported extension for %q (allowed: %v)", mediaURL, supportedAttachmentExts)
	}

	data, contentType, err := downloadFile(ctx, mediaURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", mediaURL, err)
	}

	return sendMediaData(ctx, client, toUserID, filenameFromURL(mediaURL), mediaURL, data, contentType, contextToken)
}

// sendMediaFromPath reads a local file and sends it as a media message via
// the iLink CDN path.
func sendMediaFromPath(ctx context.Context, client *ilink.Client, toUserID, path, contextToken string) error {
	if !isSupportedAttachmentExt(path) {
		return fmt.Errorf("wechat/media: unsupported extension for %q (allowed: %v)", path, supportedAttachmentExts)
	}

	sb := fileserver.GetFileManager()
	info, err := sb.FileStat(&fsmodel.FileStatRequest{Path: path})
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir {
		return fmt.Errorf("wechat/media: %q is a directory", path)
	}
	if info.Size > int64(math.MaxInt) {
		return fmt.Errorf("wechat/media: %q is too large", path)
	}
	contentType := inferContentType(path)
	_, itemType := classifyMedia(contentType, path)
	if err := checkAttachmentSize(itemType, int(info.Size)); err != nil {
		return err
	}

	// Read directly into a byte slice; previously we asked FileRead to
	// base64-encode, then decoded back, which transiently kept ~2.3× the
	// file size in memory (raw + base64 string + decoded bytes). The path
	// here is always a host-local file, so os.ReadFile is the cheapest way.
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	return sendMediaData(ctx, client, toUserID, filepath.Base(path), path, data, contentType, contextToken)
}

func sendMediaData(ctx context.Context, client *ilink.Client, toUserID, fileName, source string, data []byte, contentType, contextToken string) error {
	if fileName == "" {
		fileName = "file"
	}

	cdnMediaType, itemType := classifyMedia(contentType, source)

	if err := checkAttachmentSize(itemType, len(data)); err != nil {
		return err
	}

	logger.Debugf(ctx, "[wechat/media] uploading %s (%s, %d bytes) for %s", source, contentType, len(data), toUserID)

	uploaded, err := cdn.Upload(ctx, client, data, toUserID, cdnMediaType)
	if err != nil {
		return fmt.Errorf("upload to CDN: %w", err)
	}

	media := &ilink.MediaInfo{
		EncryptQueryParam: uploaded.DownloadParam,
		AESKey:            cdn.HexStringAsBase64(uploaded.AESKeyHex),
		EncryptType:       1,
	}

	var item ilink.MessageItem
	switch itemType {
	case ilink.ItemTypeImage:
		item = ilink.MessageItem{
			Type: ilink.ItemTypeImage,
			ImageItem: &ilink.ImageItem{
				Media:   media,
				MidSize: uploaded.CipherSize,
			},
		}
	case ilink.ItemTypeVideo:
		item = ilink.MessageItem{
			Type: ilink.ItemTypeVideo,
			VideoItem: &ilink.VideoItem{
				Media:     media,
				VideoSize: uploaded.CipherSize,
			},
		}
	default:
		item = ilink.MessageItem{
			Type: ilink.ItemTypeFile,
			FileItem: &ilink.FileItem{
				Media:    media,
				FileName: fileName,
				Len:      fmt.Sprintf("%d", uploaded.FileSize),
			},
		}
	}

	req := &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID:   client.BotID(),
			ToUserID:     toUserID,
			ClientID:     newClientID(),
			MessageType:  ilink.MessageTypeBot,
			MessageState: ilink.MessageStateFinish,
			ItemList:     []ilink.MessageItem{item},
			ContextToken: contextToken,
		},
		BaseInfo: ilink.BaseInfo{},
	}

	resp, err := client.SendMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("send media message: %w", err)
	}
	if resp.Ret != 0 {
		return fmt.Errorf("send media failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
	}

	logger.Debugf(ctx, "[wechat/media] sent %s to %s from %s", contentType, toUserID, source)
	return nil
}

func downloadFile(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("parse URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "", fmt.Errorf("wechat/media: unsupported scheme %q", u.Scheme)
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}

	// externalHTTPClient has its own Transport with an SSRF-blocking
	// DialContext — so a markdown ![](http://127.0.0.1/...) in LLM output
	// cannot reach internal services. Separate from cdnHTTPClient so a slow
	// external download doesn't starve the CDN connection pool either.
	resp, err := externalHTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	if resp.ContentLength > maxDownloadBytes {
		return nil, "", fmt.Errorf("remote file too large: %d bytes (max %d)",
			resp.ContentLength, maxDownloadBytes)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxDownloadBytes {
		return nil, "", fmt.Errorf("remote file exceeds %d bytes", maxDownloadBytes)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = inferContentType(rawURL)
	}

	return data, contentType, nil
}

// externalHTTPClient fetches arbitrary user-supplied URLs (the ![](url) path
// in LLM output). It has its own Transport — separate from cdnHTTPClient —
// so (1) a slow external download cannot starve the CDN connection pool,
// and (2) the DialContext refuses non-public addresses. Without the dial
// guard, an agent could be tricked into hitting 169.254.169.254 (cloud
// metadata), 127.0.0.1:6379 (local Redis), or anything in 10.0.0.0/8.
var externalHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
			// Use a trampoline closure rather than passing the function value
			// directly so tests can swap rejectNonPublicAddress at runtime.
			Control: func(network, address string, c syscall.RawConn) error {
				return rejectNonPublicAddress(network, address, c)
			},
		}).DialContext,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
}

// cgnShared is RFC 6598 "Shared Address Space" (100.64.0.0/10), used by
// Carrier-Grade NAT, cloud provider internal pools and some corporate
// fabrics. net.IP.IsPrivate() does not cover it.
var cgnShared = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

// rejectNonPublicAddress is the Transport-level SSRF guard. It runs *after*
// DNS resolution, so it closes the TOCTOU hole that a URL-level pre-check
// would leave open (a malicious DNS record that returns a public IP during
// the check and 127.0.0.1 at dial time). Declared as a var so tests can
// swap in a no-op when hitting an httptest.Server on 127.0.0.1.
var rejectNonPublicAddress = func(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("wechat/media: bad dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("wechat/media: unresolved host %q", host)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("wechat/media: blocked non-public address %s", ip)
	}
	if v4 := ip.To4(); v4 != nil && cgnShared.Contains(v4) {
		return fmt.Errorf("wechat/media: blocked CGN address %s", ip)
	}
	return nil
}

func classifyMedia(contentType, url string) (cdnMediaType int, itemType int) {
	ct := strings.ToLower(contentType)

	if strings.HasPrefix(ct, "image/") || isImageExt(url) {
		return ilink.CDNMediaTypeImage, ilink.ItemTypeImage
	}
	if strings.HasPrefix(ct, "video/") || isVideoExt(url) {
		return ilink.CDNMediaTypeVideo, ilink.ItemTypeVideo
	}
	return ilink.CDNMediaTypeFile, ilink.ItemTypeFile
}

func isImageExt(url string) bool {
	ext := strings.ToLower(filepath.Ext(stripQuery(url)))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

func isVideoExt(url string) bool {
	ext := strings.ToLower(filepath.Ext(stripQuery(url)))
	switch ext {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi":
		return true
	}
	return false
}

func inferContentType(url string) string {
	ext := filepath.Ext(stripQuery(url))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func filenameFromURL(rawURL string) string {
	u := stripQuery(rawURL)
	name := filepath.Base(u)
	if name == "" || name == "." || name == "/" {
		return "file"
	}
	return name
}

func stripQuery(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}
