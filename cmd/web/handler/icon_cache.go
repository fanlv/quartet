package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/path"
)

func (h *Handler) IconProxy(ctx context.Context, c *app.RequestContext) {
	url := strings.TrimSpace(string(c.Query("url")))
	if url == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	cacheDir, err := path.IconCacheDir()
	if err != nil {
		logger.Errorf(ctx, "[icon-cache] cache dir error: %v", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		logger.Errorf(ctx, "[icon-cache] mkdir error: %v", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	hash := sha256.Sum256([]byte(url))
	key := hex.EncodeToString(hash[:])
	metaPath := filepath.Join(cacheDir, key+".meta")
	dataPath := filepath.Join(cacheDir, key+".data")

	if info, statErr := os.Stat(dataPath); statErr == nil && info.Size() > 0 {
		if time.Since(info.ModTime()) < 24*time.Hour {
			contentType := "image/png"
			if meta, readErr := os.ReadFile(metaPath); readErr == nil && len(meta) > 0 {
				contentType = string(meta)
			}
			c.Header("Content-Type", contentType)
			c.Header("Cache-Control", "public, max-age=86400")
			c.File(dataPath)
			return
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if reqErr != nil {
		logger.Warnf(ctx, "[icon-cache] bad request url=%s err=%v", url, reqErr)
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	resp, fetchErr := client.Do(req)
	if fetchErr != nil {
		logger.Warnf(ctx, "[icon-cache] fetch failed url=%s err=%v", url, fetchErr)
		c.Redirect(http.StatusTemporaryRedirect, []byte(url))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Warnf(ctx, "[icon-cache] upstream status=%d url=%s", resp.StatusCode, url)
		c.Redirect(http.StatusTemporaryRedirect, []byte(url))
		return
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if readErr != nil {
		logger.Warnf(ctx, "[icon-cache] read body failed url=%s err=%v", url, readErr)
		c.Redirect(http.StatusTemporaryRedirect, []byte(url))
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/png"
	}

	_ = os.WriteFile(dataPath, body, 0o644)
	_ = os.WriteFile(metaPath, []byte(contentType), 0o644)

	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, contentType, body)
}

func IconCacheURL(originalURL string) string {
	if originalURL == "" {
		return ""
	}
	if !strings.HasPrefix(originalURL, "http://") && !strings.HasPrefix(originalURL, "https://") {
		return originalURL
	}
	return fmt.Sprintf("/api/v1/icon?url=%s", originalURL)
}
