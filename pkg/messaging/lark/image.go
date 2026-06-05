package lark

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
	"github.com/fanlv/quartet/pkg/messaging/media"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// maxLarkImageBytes caps image downloads to protect against a runaway
// response body. Lark's own upload cap is 30 MB, so 40 MB leaves headroom
// while still refusing clearly-out-of-spec payloads.
const maxLarkImageBytes = 40 * 1024 * 1024

// maxLarkImageParallel bounds how many image downloads run concurrently per
// post. Without a cap, a post with N embedded images fans out N goroutines
// that each buffer up to maxLarkImageBytes — a trivial way to spike memory on
// the IM listener goroutine.
const maxLarkImageParallel = 4

// downloadPostImages downloads every unique image referenced by the post in
// parallel so a multi-image message doesn't serialize N × 30s of HTTP calls
// on the SDK event-dispatch goroutine. Returns a map from imageKey to local
// file path; failed downloads map to "" so the caller can emit a fallback
// placeholder.
func (l *Listener) downloadPostImages(ctx context.Context, messageID string, post *rawPostContent) map[string]string {
	keys := uniquePostImageKeys(post)
	if len(keys) == 0 {
		return nil
	}

	dir, err := media.CacheDir()
	if err != nil {
		logger.Warn("[lark] resolve media cache dir failed: %v", err)
		return nil
	}

	results := make(map[string]string, len(keys))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxLarkImageParallel)
	for _, key := range keys {
		// Respect ctx cancellation while waiting on the semaphore so a
		// cancelled request (shutdown, upstream timeout) doesn't pin the
		// listener goroutine behind a full semaphore.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return results
		}
		wg.Add(1)
		go func(imageKey string) {
			defer wg.Done()
			defer func() { <-sem }()
			localPath, err := l.downloadImageToTemp(ctx, messageID, imageKey, dir)
			if err != nil {
				logger.Warn("[lark] download image failed: msg=%s key=%s err=%v", messageID, imageKey, err)
			}
			mu.Lock()
			results[imageKey] = localPath
			mu.Unlock()
		}(key)
	}
	wg.Wait()
	return results
}

func uniquePostImageKeys(post *rawPostContent) []string {
	seen := make(map[string]struct{})
	var keys []string
	for _, paragraph := range post.Content {
		for _, elem := range paragraph {
			if elem.Tag != "img" || elem.ImageKey == "" {
				continue
			}
			if _, ok := seen[elem.ImageKey]; ok {
				continue
			}
			seen[elem.ImageKey] = struct{}{}
			keys = append(keys, elem.ImageKey)
		}
	}
	return keys
}

func (l *Listener) renderImageReference(ctx context.Context, messageID string, imageKey string) string {
	if imageKey == "" {
		return "![image](#)"
	}

	dir, err := media.CacheDir()
	if err != nil {
		logger.Warn("[lark] resolve media cache dir failed: %v", err)
		return "![image](# \"image_key=" + imageKey + "\")"
	}
	localPath, err := l.downloadImageToTemp(ctx, messageID, imageKey, dir)
	if err != nil {
		logger.Warn("[lark] download image failed: msg=%s key=%s err=%v", messageID, imageKey, err)
	}
	return renderImageLocalPath(imageKey, localPath)
}

// renderImageLocalPath formats an image reference for agent-visible output:
// the markdown image link when the download succeeded, or a fallback
// placeholder that still carries the imageKey so downstream code can retry.
func renderImageLocalPath(imageKey, localPath string) string {
	if imageKey == "" {
		return "![image](#)"
	}
	if localPath != "" {
		return "![image](" + localPath + ")"
	}
	// Keep markdown shape even when the download failed, but preserve the
	// image_key for troubleshooting and potential downstream retries.
	return "![image](# \"image_key=" + imageKey + "\")"
}

func (l *Listener) downloadImageToTemp(ctx context.Context, messageID string, imageKey string, dir string) (string, error) {
	if l.imageDownloader != nil {
		return l.imageDownloader(ctx, messageID, imageKey)
	}

	// Bound download time so a stuck HTTP connection doesn't leak a goroutine
	// for the lifetime of the WebSocket (SDK uses http.DefaultClient w/o timeout).
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client, err := l.newOpenClient()
	if err != nil {
		return "", err
	}

	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(imageKey).
		Type("image").
		Build()

	resp, err := client.Im.V1.MessageResource.Get(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark API error: code=%d msg=%s", resp.Code, resp.Msg)
	}

	fileName := resp.FileName
	if fileName == "" {
		fileName = imageKey
	}
	fileName = messaging.SanitizeFileNamePart(filepath.Base(fileName))
	if fileName == "" {
		fileName = messaging.SanitizeFileNamePart(imageKey)
	}
	if fileName == "" {
		fileName = "image"
	}

	// Prefix with messageID+imageKey to guarantee uniqueness — Lark returns
	// the original file name which can easily collide across different
	// messages (e.g. every screenshot is "image.png").
	safeKey := messaging.SanitizeFileNamePart(messageID + "_" + imageKey)
	if safeKey == "" {
		safeKey = "lark_image"
	}
	fileName = safeKey + "_" + fileName
	// Clamp to a safe byte length — ext4 caps file names at 255 bytes and
	// multi-byte (Chinese/emoji) filenames can easily overrun once combined
	// with messageID + imageKey. Preserve the extension so downstream tools
	// still pick the right MIME type.
	fileName = messaging.CapFileNameBytes(fileName, 200)

	destPath := filepath.Join(dir, fileName)
	if err := writeReaderToFile(destPath, resp.File); err != nil {
		return "", err
	}
	return destPath, nil
}

func writeReaderToFile(filePath string, r io.Reader) error {
	dir := filepath.Dir(filePath)
	sb := fileserver.GetFileManager()
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Stream to disk via FileUpload (which io.Copy's into a temp file and
	// renames into place) instead of buffering the whole image into memory
	// and base64-encoding it. The +1 lets us distinguish "hit the cap
	// exactly" from "overran the cap".
	limited := io.LimitReader(r, maxLarkImageBytes+1)
	res, err := sb.FileUpload(filepath.Base(filePath), limited, filePath)
	if err != nil {
		return fmt.Errorf("write %s: %w", filePath, err)
	}
	if res != nil && res.Size > maxLarkImageBytes {
		_ = sb.FileDelete(&fsmodel.FileDeleteRequest{Path: filePath})
		return fmt.Errorf("image exceeds %d bytes", maxLarkImageBytes)
	}
	return nil
}
