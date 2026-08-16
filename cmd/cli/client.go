package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

const defaultBaseURL = "http://127.0.0.1:8090"

// client talks to the Quartet web backend's graph-workflow API.
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

func newClient() *client {
	base := strings.TrimRight(os.Getenv("QUARTET_BASE_URL"), "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &client{
		baseURL: base,
		token:   firstAuthToken(os.Getenv(consts.EnvKeyAgentAuth)),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// firstAuthToken picks the first non-empty token from the comma-separated
// X_AGENT_AUTH value. The backend accepts any single token from the list but
// compares against the WHOLE header value, so sending the raw joined string
// would never match (same convention as agent-browser's `${X_AGENT_AUTH%%,*}`).
func firstAuthToken(raw string) string {
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			return t
		}
	}
	return ""
}

// do issues a request and returns the raw body. A non-2xx status is turned into
// an error carrying the full response body, so backend validation/auth errors
// reach the user verbatim (per the repo "show errors in full" convention).
func (c *client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set(consts.HeaderAgentAuth, c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s failed: %w", method, c.baseURL+path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return respBody, fmt.Errorf("backend returned %s for %s %s: %s", resp.Status, method, path, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

func (c *client) createWorkflow(ctx context.Context, req *model.CreateGraphWorkflowRequest) (*model.GraphWorkflowResponse, error) {
	raw, err := c.do(ctx, http.MethodPost, "/api/v1/graph/workflow", req)
	if err != nil {
		return nil, err
	}
	return decode[model.GraphWorkflowResponse](raw)
}

func (c *client) listWorkflows(ctx context.Context) (*model.GraphListWorkflowsResponse, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/graph/workflow/list", nil)
	if err != nil {
		return nil, err
	}
	return decode[model.GraphListWorkflowsResponse](raw)
}

func (c *client) getWorkflow(ctx context.Context, id string) (*model.GraphWorkflowResponse, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/graph/workflow/"+id, nil)
	if err != nil {
		return nil, err
	}
	return decode[model.GraphWorkflowResponse](raw)
}

func (c *client) updateWorkflow(ctx context.Context, id string, req *model.UpdateGraphWorkflowRequest) (*model.GraphWorkflowResponse, error) {
	raw, err := c.do(ctx, http.MethodPut, "/api/v1/graph/workflow/"+id, req)
	if err != nil {
		return nil, err
	}
	return decode[model.GraphWorkflowResponse](raw)
}

func (c *client) deleteWorkflow(ctx context.Context, id string, req *model.DeleteGraphWorkflowRequest) error {
	_, err := c.do(ctx, http.MethodDelete, "/api/v1/graph/workflow/"+id, req)
	return err
}

func (c *client) validateConfig(ctx context.Context, req *model.ValidateGraphWorkflowRequest) (*model.GraphValidationResponse, error) {
	raw, err := c.do(ctx, http.MethodPost, "/api/v1/graph/workflow/validate", req)
	if err != nil {
		return nil, err
	}
	return decode[model.GraphValidationResponse](raw)
}

func (c *client) sendWeChat(ctx context.Context, req *model.WeChatSendMessageRequest) (*model.WeChatSendMessageResponse, error) {
	raw, err := c.do(ctx, http.MethodPost, "/api/v1/wechat/send", req)
	if err != nil {
		return nil, err
	}
	return decode[model.WeChatSendMessageResponse](raw)
}

func (c *client) verifyWeChatOutbox(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/api/v1/wechat/outbox/status", nil)
	if err != nil {
		return fmt.Errorf("backend does not expose the durable WeChat outbox; restart the updated quartet-web before sending: %w", err)
	}
	return nil
}

func (c *client) getWeChatOutbox(ctx context.Context, taskID string) (*model.WeChatOutboxResultResponse, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/wechat/outbox/"+taskID, nil)
	if err != nil {
		return nil, err
	}
	return decode[model.WeChatOutboxResultResponse](raw)
}

// decode unmarshals a JSON body into T, wrapping parse errors with the raw body
// so a malformed/unexpected response is visible.
func decode[T any](raw []byte) (*T, error) {
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w (body: %s)", err, strings.TrimSpace(string(raw)))
	}
	return &out, nil
}
