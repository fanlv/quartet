package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fanlv/quartet/types/model"
)

const defaultBaseURL = "http://127.0.0.1:8090"

// client talks to the Quartet web backend's graph-workflow API.
type client struct {
	baseURL       string
	sessionCookie string
	csrfToken     string
	credentialErr error
	http          *http.Client
}

type storedSession struct {
	Cookie    string `json:"cookie"`
	CSRFToken string `json:"csrfToken"`
}
type sessionStore struct {
	Sessions map[string]storedSession `json:"sessions"`
}

func newClient() *client {
	base := strings.TrimRight(os.Getenv("QUARTET_BASE_URL"), "/")
	if base == "" {
		base = defaultBaseURL
	}
	stored, err := loadStoredSession(base)
	return &client{baseURL: base, sessionCookie: stored.Cookie, csrfToken: stored.CSRFToken, credentialErr: err, http: &http.Client{Timeout: 30 * time.Second}}
}

// do issues a request and returns the raw body. A non-2xx status is turned into
// an error carrying the full response body, so backend validation/auth errors
// reach the user verbatim (per the repo "show errors in full" convention).
func (c *client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	if c.credentialErr != nil {
		return nil, c.credentialErr
	}
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
	if c.sessionCookie != "" {
		req.AddCookie(&http.Cookie{Name: "quartet_session", Value: c.sessionCookie})
	}
	if method != http.MethodGet && method != http.MethodHead && c.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", c.csrfToken)
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

func sessionFile() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "quartet", "sessions.json"), nil
}
func readSessionStore() (sessionStore, string, error) {
	path, err := sessionFile()
	if err != nil {
		return sessionStore{}, "", err
	}
	store := sessionStore{Sessions: map[string]storedSession{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, path, nil
	}
	if err != nil {
		return store, path, fmt.Errorf("read session store: %w", err)
	}
	if err := json.Unmarshal(raw, &store); err != nil {
		return store, path, fmt.Errorf("parse session store %s: %w", path, err)
	}
	if store.Sessions == nil {
		store.Sessions = map[string]storedSession{}
	}
	return store, path, nil
}
func loadStoredSession(baseURL string) (storedSession, error) {
	store, _, err := readSessionStore()
	if err != nil {
		return storedSession{}, err
	}
	return store.Sessions[baseURL], nil
}
func saveStoredSession(baseURL string, session storedSession) error {
	store, path, err := readSessionStore()
	if err != nil {
		return err
	}
	if session.Cookie == "" {
		delete(store.Sessions, baseURL)
	} else {
		store.Sessions[baseURL] = session
	}
	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "sessions-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
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

// get issues a GET with optional query parameters.
func (c *client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *client) startGraphRun(ctx context.Context, req *model.StartGraphRunRequest) (*model.GraphRunResponse, error) {
	raw, err := c.do(ctx, http.MethodPost, "/api/v1/graph/run/start", req)
	if err != nil {
		return nil, err
	}
	return decode[model.GraphRunResponse](raw)
}

func (c *client) createSchedule(ctx context.Context, req *model.CreateScheduleRequest) (*model.CreateScheduleResponse, error) {
	raw, err := c.do(ctx, http.MethodPost, "/api/v1/schedule/create", req)
	if err != nil {
		return nil, err
	}
	return decode[model.CreateScheduleResponse](raw)
}

func (c *client) listSchedules(ctx context.Context, workspaceID string) (*model.ListSchedulesResponse, error) {
	q := url.Values{}
	if workspaceID != "" {
		q.Set("workspaceId", workspaceID)
	}
	raw, err := c.get(ctx, "/api/v1/schedule/list", q)
	if err != nil {
		return nil, err
	}
	return decode[model.ListSchedulesResponse](raw)
}

// getSchedule fetches one task. The backend returns a bare ScheduleInfo (no
// envelope) for get / update / toggle.
func (c *client) getSchedule(ctx context.Context, id string) (*model.ScheduleInfo, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/schedule/"+id, nil)
	if err != nil {
		return nil, err
	}
	return decode[model.ScheduleInfo](raw)
}

func (c *client) updateSchedule(ctx context.Context, id string, req *model.UpdateScheduleRequest) (*model.ScheduleInfo, error) {
	raw, err := c.do(ctx, http.MethodPut, "/api/v1/schedule/"+id, req)
	if err != nil {
		return nil, err
	}
	return decode[model.ScheduleInfo](raw)
}

func (c *client) deleteSchedule(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/api/v1/schedule/"+id, nil)
	return err
}

func (c *client) toggleSchedule(ctx context.Context, id string) (*model.ScheduleInfo, error) {
	raw, err := c.do(ctx, http.MethodPost, "/api/v1/schedule/"+id+"/toggle", nil)
	if err != nil {
		return nil, err
	}
	return decode[model.ScheduleInfo](raw)
}

// scheduleRunResponse is the backend's reply to POST /schedule/:id/run.
type scheduleRunResponse struct {
	Status string `json:"status"`
	JobID  string `json:"jobId"`
}

func (c *client) runSchedule(ctx context.Context, id string) (*scheduleRunResponse, error) {
	raw, err := c.do(ctx, http.MethodPost, "/api/v1/schedule/"+id+"/run", nil)
	if err != nil {
		return nil, err
	}
	return decode[scheduleRunResponse](raw)
}

func (c *client) listWorkspaces(ctx context.Context) (*model.ListWorkspacesResponse, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/workspace/list", nil)
	if err != nil {
		return nil, err
	}
	return decode[model.ListWorkspacesResponse](raw)
}

func (c *client) listJobs(ctx context.Context, workspaceID string, limit int) (*model.ListJobsResponse, error) {
	q := url.Values{}
	if workspaceID != "" {
		q.Set("workspaceId", workspaceID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	raw, err := c.get(ctx, "/api/v1/job/list", q)
	if err != nil {
		return nil, err
	}
	return decode[model.ListJobsResponse](raw)
}

// getJob returns the raw response body: the endpoint emits the Job fields
// flattened at the root plus a lastEventSeq field (the SSE resume cursor), and
// the CLI prints it verbatim rather than re-modeling the envelope.
func (c *client) getJob(ctx context.Context, id string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, "/api/v1/job/"+id, nil)
}

func (c *client) stopJob(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/v1/job/"+id+"/stop", nil)
	return err
}

func (c *client) listAgents(ctx context.Context) (*model.AgentListResponse, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/agent/list", nil)
	if err != nil {
		return nil, err
	}
	return decode[model.AgentListResponse](raw)
}

func (c *client) wechatAccounts(ctx context.Context) (*wechatAccountsResponse, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api/v1/wechat/accounts", nil)
	if err != nil {
		return nil, err
	}
	return decode[wechatAccountsResponse](raw)
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
