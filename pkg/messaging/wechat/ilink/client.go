package ilink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL  = "https://ilinkai.weixin.qq.com"
	longPollTimeout = 35 * time.Second
	sendTimeout     = 15 * time.Second
	// maxResponseBytes caps ilink API JSON responses. Realistic GetUpdates
	// batches are <200 KB (metadata only — media is served from CDN, not
	// inline), so 10 MiB is far above any legitimate payload while still
	// bounding memory if iLink or a MITM returns a runaway stream.
	maxResponseBytes = 10 * 1024 * 1024
)

// requestLimiter throttles outbound iLink HTTP calls to reduce ban risk. The
// cap (1 req/s steady, burst 3) is per doc §9.1. It is package-level so every
// Client shares the budget — two Clients for the same bot (listener + replier)
// must not double the effective QPS. Long-poll getupdates is subject to the
// same limit, but since each call occupies the server for ~35s the token
// bucket has plenty of time to refill between requests; burst=3 also absorbs
// rapid reconnect storms during error backoff.
var requestLimiter = rate.NewLimiter(rate.Every(time.Second), 3)

// Client is an iLink HTTP API client.
type Client struct {
	baseURL    string
	botToken   string
	botID      string
	httpClient *http.Client
	wechatUIN  string
	limiter    *rate.Limiter
}

// NewClient creates a new iLink API client bound to the given credentials.
func NewClient(creds *Credentials) *Client {
	baseURL := creds.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	c := &Client{
		baseURL:  baseURL,
		botToken: creds.BotToken,
		botID:    creds.ILinkBotID,
		// Per-call context deadlines are the primary timeout mechanism (see
		// GetUpdates / SendMessage / …). The client-level Timeout is a
		// belt-and-braces last-resort cap in case a future method forgets to
		// set a deadline — it only kicks in above the longest legitimate
		// call (getupdates long-poll is ~40s including slack), so normal
		// traffic never bumps into it.
		httpClient: &http.Client{Timeout: 120 * time.Second},
		wechatUIN:  generateWechatUIN(),
		limiter:    requestLimiter,
	}
	return c
}

// newUnauthenticatedClient creates a client without credentials for the login
// flow (FetchQRCode / PollQRStatus).
func newUnauthenticatedClient() *Client {
	return &Client{
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: 40 * time.Second},
		wechatUIN:  generateWechatUIN(),
		limiter:    requestLimiter,
	}
}

// BotID returns the bot's iLink user ID (the `@xxxxx_yyyyy` form).
func (c *Client) BotID() string {
	return c.botID
}

// BaseURL returns the upstream base URL for CDN / API operations.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// GetUpdates performs a long-poll for new messages.
func (c *Client) GetUpdates(ctx context.Context, buf string) (*GetUpdatesResponse, error) {
	reqBody := GetUpdatesRequest{
		GetUpdatesBuf: buf,
		BaseInfo:      BaseInfo{ChannelVersion: "1.0.0"},
	}

	ctx, cancel := context.WithTimeout(ctx, longPollTimeout+5*time.Second)
	defer cancel()

	var resp GetUpdatesResponse
	if err := c.doPost(ctx, "/ilink/bot/getupdates", reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SendMessage sends a message through iLink.
func (c *Client) SendMessage(ctx context.Context, msg *SendMessageRequest) (*SendMessageResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	var resp SendMessageResponse
	if err := c.doPost(ctx, "/ilink/bot/sendmessage", msg, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetConfig fetches bot config for a user (includes typing_ticket).
func (c *Client) GetConfig(ctx context.Context, userID, contextToken string) (*GetConfigResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req := GetConfigRequest{
		ILinkUserID:  userID,
		ContextToken: contextToken,
		BaseInfo:     BaseInfo{},
	}

	var resp GetConfigResponse
	if err := c.doPost(ctx, "/ilink/bot/getconfig", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SendTyping sends a typing indicator to a user.
func (c *Client) SendTyping(ctx context.Context, userID, typingTicket string, status int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req := SendTypingRequest{
		ILinkUserID:  userID,
		TypingTicket: typingTicket,
		Status:       status,
		BaseInfo:     BaseInfo{},
	}

	var resp SendTypingResponse
	if err := c.doPost(ctx, "/ilink/bot/sendtyping", req, &resp); err != nil {
		return err
	}
	if resp.Ret != 0 {
		return fmt.Errorf("sendtyping failed: ret=%d errmsg=%s", resp.Ret, resp.ErrMsg)
	}
	return nil
}

// GetUploadURL gets a pre-signed CDN upload URL for media files.
func (c *Client) GetUploadURL(ctx context.Context, req *GetUploadURLRequest) (*GetUploadURLResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	var resp GetUploadURLResponse
	if err := c.doPost(ctx, "/ilink/bot/getuploadurl", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) doPost(ctx context.Context, path string, body interface{}, result interface{}) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ilink POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("ilink POST %s read response: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ilink POST %s HTTP %d: %s", path, resp.StatusCode, truncate(string(respBody), 512))
	}

	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("ilink POST %s unmarshal response: %w", path, err)
	}
	return nil
}

func (c *Client) doGet(ctx context.Context, url string, result interface{}) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ilink GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("ilink GET %s read response: %w", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ilink GET %s HTTP %d: %s", url, resp.StatusCode, truncate(string(respBody), 512))
	}

	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("ilink GET %s unmarshal response: %w", url, err)
	}
	return nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Authorization", "Bearer "+c.botToken)
	req.Header.Set("X-WECHAT-UIN", c.wechatUIN)
}

func generateWechatUIN() string {
	var n uint32
	_ = binary.Read(rand.Reader, binary.LittleEndian, &n)
	s := fmt.Sprintf("%d", n)
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// truncate clips s to at most n bytes, appending an ellipsis marker when it
// was shortened. Used to keep HTTP error bodies from blowing up error logs.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
