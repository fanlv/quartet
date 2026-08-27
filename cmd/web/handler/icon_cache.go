package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/path"
)

func (h *Handler) IconProxy(ctx context.Context, c *app.RequestContext) {
	iconURL := strings.TrimSpace(string(c.Query("url")))
	if iconURL == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !isRemoteIconURL(iconURL) {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	h.serveRemoteIcon(ctx, c, iconURL)
}

// PublicJobAgentIcon serves one catalog icon only after the share middleware
// has validated the Job token and this handler has verified that the requested
// Agent is actually referenced by that Job. The caller never supplies an
// upstream URL, so the public endpoint cannot be used as a general SSRF proxy.
func (h *Handler) PublicJobAgentIcon(ctx context.Context, c *app.RequestContext) {
	job, ok := getPublicJob(c)
	if !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	agentID := strings.TrimSpace(c.Param("agentId"))
	if agentID == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	agents, err := h.agentCatalog.ResolveDisplayInfos(ctx, h.collectJobAgentRefs(ctx, job))
	if err != nil {
		logger.Errorf(ctx, "[icon-cache] resolve public Agent failed: jobId=%s agentId=%s err=%v", job.ID, agentID, err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	for _, info := range agents {
		if info.AgentID == agentID && isRemoteIconURL(info.IconURL) {
			h.serveRemoteIcon(ctx, c, info.IconURL)
			return
		}
	}
	c.AbortWithStatus(http.StatusNotFound)
}

func isRemoteIconURL(iconURL string) bool {
	return strings.HasPrefix(iconURL, "http://") || strings.HasPrefix(iconURL, "https://")
}

func (h *Handler) serveRemoteIcon(ctx context.Context, c *app.RequestContext, iconURL string) {

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

	hash := sha256.Sum256([]byte(iconURL))
	key := hex.EncodeToString(hash[:])
	metaPath := filepath.Join(cacheDir, key+".meta")
	dataPath := filepath.Join(cacheDir, key+".data")

	if info, statErr := os.Stat(dataPath); statErr == nil && info.Size() > 0 {
		if time.Since(info.ModTime()) < 24*time.Hour {
			contentType := ""
			if meta, readErr := os.ReadFile(metaPath); readErr == nil && len(meta) > 0 {
				contentType = string(meta)
			}
			if strings.HasPrefix(contentType, "image/") {
				c.Header("Content-Type", contentType)
				c.Header("Cache-Control", "private, max-age=86400")
				c.File(dataPath)
				return
			}
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	if transport := proxyTransport(); transport != nil {
		client.Transport = transport
	}
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, iconURL, nil)
	if reqErr != nil {
		logger.Warnf(ctx, "[icon-cache] bad request url=%s err=%v", iconURL, reqErr)
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	resp, fetchErr := client.Do(req)
	if fetchErr != nil {
		logger.Warnf(ctx, "[icon-cache] fetch failed url=%s err=%v", iconURL, fetchErr)
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Warnf(ctx, "[icon-cache] upstream status=%d url=%s", resp.StatusCode, iconURL)
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}

	const maxIconBytes = 2 * 1024 * 1024
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if readErr != nil {
		logger.Warnf(ctx, "[icon-cache] read body failed url=%s err=%v", iconURL, readErr)
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	if len(body) > maxIconBytes {
		logger.Warnf(ctx, "[icon-cache] image exceeds 2 MiB url=%s", iconURL)
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if mediaType, _, parseErr := mime.ParseMediaType(contentType); parseErr == nil {
		contentType = mediaType
	}
	if !strings.HasPrefix(contentType, "image/") {
		contentType = http.DetectContentType(body)
	}
	if !strings.HasPrefix(contentType, "image/") {
		logger.Warnf(ctx, "[icon-cache] upstream is not an image: contentType=%s url=%s", contentType, iconURL)
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}

	_ = os.WriteFile(dataPath, body, 0o644)
	_ = os.WriteFile(metaPath, []byte(contentType), 0o644)

	c.Header("Content-Type", contentType)
	c.Header("Cache-Control", "private, max-age=86400")
	c.Data(http.StatusOK, contentType, body)
}

func PublicAgentIconURL(jobID, agentID string) string {
	return fmt.Sprintf(
		"/api/v1/public/job/%s/agents/%s/icon",
		url.PathEscape(jobID),
		url.PathEscape(agentID),
	)
}

func IconCacheURL(originalURL string) string {
	if originalURL == "" {
		return ""
	}
	if !strings.HasPrefix(originalURL, "http://") && !strings.HasPrefix(originalURL, "https://") {
		return originalURL
	}
	// QueryEscape matters: callers append their own credential parameter
	// (session cookie for the private route, &shareToken=&jobId= for the public one),
	// so an unescaped '&' inside originalURL would truncate the upstream URL
	// and let it swallow whatever is appended after it.
	return fmt.Sprintf("/api/v1/icon?url=%s", url.QueryEscape(originalURL))
}

func proxyTransport() *http.Transport {
	proxyURL := os.Getenv("HTTP_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("http_proxy")
	}
	if proxyURL == "" {
		proxyURL = os.Getenv("HTTPS_PROXY")
	}
	if proxyURL == "" {
		proxyURL = os.Getenv("https_proxy")
	}
	if proxyURL == "" {
		return nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil
	}
	return &http.Transport{
		Proxy: http.ProxyURL(parsed),
	}
}
