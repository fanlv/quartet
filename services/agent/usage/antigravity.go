package usage

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

const (
	agyBin = "agy"
	// antigravityQuotaPath is the Connect-RPC method on the agy language server
	// that returns the per-model-group plan quota.
	antigravityQuotaPath = "/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary"
	// antigravityQuotaBody is the metadata envelope the quota RPC expects.
	antigravityQuotaBody = `{"metadata":{"ideName":"antigravity","extensionName":"antigravity","ideVersion":"unknown","locale":"en"}}`
	// antigravityCSRFHeader is the language-server CSRF header. Antigravity 2.x
	// rejects RetrieveUserQuotaSummary with HTTP 401 "missing CSRF token" unless
	// this is set. Older agy CLI builds still accept an empty token.
	antigravityCSRFHeader = "X-Codeium-Csrf-Token"
	// antigravityCacheMaxAge bounds how long a cached quota stays usable when its
	// windows carry no reset time. Mirrors TokenTracker's 7-day cache ceiling.
	antigravityCacheMaxAge = 7 * 24 * time.Hour
	// A freshly started agy opens its ports quickly, but its OAuth-backed quota
	// state takes a few seconds to become available.
	antigravityStartupTimeout = 7 * time.Second
	antigravityRetryInterval  = 250 * time.Millisecond
)

// antigravityEndpoint is one loopback port that may serve the quota RPC, plus
// CSRF tokens taken from the owning process command line (empty for older CLI).
type antigravityEndpoint struct {
	port int
	csrf []string
}

// AntigravityUsage reads the agy plan quota plus the agy CLI version. agy runs as
// a local language server: each agy process listens on loopback ports that serve
// the Connect-RPC quota endpoint. Current CLI builds expose that RPC on HTTPS
// with a self-signed loopback cert; older builds still have a plaintext HTTP
// port. Antigravity 2.x may also guard the RPC with a CSRF token from
// `--csrf_token` / `--extension_server_csrf_token` or the HTML page at `/`.
// We resolve ports via ps + lsof and POST (HTTPS first, then HTTP) until one
// answers.
//
// agy is a short-lived process (antigravity-acp spawns it per request), so a poll
// that catches no live+listening agy is the common case, not an error. To keep the
// UI populated across those gaps we cache the last successful quota and fall back
// to it — pruning any window whose reset time has already passed — exactly as the
// TokenTracker reference does. A hard error is only returned when the live fetch
// fails AND no usable cached quota exists.
func (s *serviceImpl) AntigravityUsage(ctx context.Context) (*model.AntigravityUsage, error) {
	// Resolve the language-server ports before starting `agy --version`.
	// Otherwise agyProcesses can observe our own short-lived version process
	// and report it as an agy server with no listening socket.
	endpoints, portsErr := s.antigravityEndpoints(ctx)
	retryQuota := false
	stopProbe := func() {}
	if portsErr != nil || len(endpoints) == 0 {
		var probeErr error
		endpoints, stopProbe, probeErr = s.startAntigravityProbe(ctx)
		if probeErr != nil {
			if portsErr != nil {
				portsErr = fmt.Errorf("%v; start temporary agy quota probe failed: %w", portsErr, probeErr)
			} else {
				portsErr = fmt.Errorf("start temporary agy quota probe failed: %w", probeErr)
			}
		} else {
			portsErr = nil
			retryQuota = true
		}
	}
	defer stopProbe()

	// The version probe runs in parallel — it is supplementary (must not add
	// serial latency to the quota RPC), and the buffered channel means the
	// goroutine never blocks even when an early error return skips the read.
	verCh := make(chan string, 1)
	go func() { verCh <- s.binVersion(ctx, agyBin) }()

	var usage *model.AntigravityUsage
	err := portsErr
	if err == nil {
		usage, err = s.antigravityLiveQuota(ctx, endpoints, retryQuota)
	}
	if err != nil {
		if cached := s.cachedAntigravityUsage(); cached != nil {
			cached.Version = <-verCh
			logger.Warnf(ctx, "[agent.usage] antigravity live quota failed (%v); serving last cached quota", err)
			return cached, nil
		}
		return nil, err
	}

	usage.Version = <-verCh
	s.storeAntigravityUsage(usage)
	return usage, nil
}

// antigravityLiveQuota queries the discovered agy ports, returning the quota
// (without Version) or an error describing why no live agy answered.
func (s *serviceImpl) antigravityLiveQuota(ctx context.Context, endpoints []antigravityEndpoint, retry bool) (*model.AntigravityUsage, error) {
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no running agy process found (is antigravity active?)")
	}

	// Loopback only: never use a proxy. Skip TLS verify because current agy
	// serves a self-signed cert on 127.0.0.1. A plaintext HTTP port still
	// works on the same client. Short timeout so a dead/mTLS port fails fast.
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				MinVersion:         tls.VersionTLS12,
			},
		},
	}

	queryCtx, cancel := context.WithCancel(ctx)
	if retry {
		queryCtx, cancel = context.WithTimeout(ctx, antigravityStartupTimeout)
	}
	defer cancel()

	type portResult struct {
		port  int
		usage *model.AntigravityUsage
		err   error
	}
	results := make(chan portResult, len(endpoints))
	for _, endpoint := range endpoints {
		go func() {
			var lastErr error
			for {
				usage, err := s.antigravityQuota(queryCtx, client, endpoint)
				if err == nil {
					results <- portResult{port: endpoint.port, usage: usage}
					return
				}
				lastErr = err
				if !retry {
					results <- portResult{port: endpoint.port, err: lastErr}
					return
				}
				select {
				case <-queryCtx.Done():
					results <- portResult{port: endpoint.port, err: lastErr}
					return
				case <-time.After(antigravityRetryInterval):
				}
			}
		}()
	}

	var lastErr error
	for range endpoints {
		result := <-results
		if result.err == nil {
			return result.usage, nil
		}
		lastErr = result.err
		logger.Warnf(ctx, "[agent.usage] antigravity quota on port %d failed: %v", result.port, result.err)
	}
	return nil, fmt.Errorf("query antigravity quota failed on all %d port(s): %w", len(endpoints), lastErr)
}

// startAntigravityProbe starts a non-generative `agy models` process solely to
// make the local quota RPC available when no prompt-time agy process is alive.
// The returned stop function terminates it after the quota has been read.
func (s *serviceImpl) startAntigravityProbe(ctx context.Context) ([]antigravityEndpoint, func(), error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(agyBin, "models")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, func() {}, fmt.Errorf("start agy models failed: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	stop := func() {
		_ = cmd.Process.Kill()
		<-waitCh
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	var lastErr error
	for {
		ports, err := s.listenPortsForPids(ctx, []int{cmd.Process.Pid})
		if err == nil && len(ports) > 0 {
			return endpointsForPorts(ports, s.processCSRFTokens(ctx, cmd.Process.Pid)), stop, nil
		}
		lastErr = err

		select {
		case err := <-waitCh:
			return nil, func() {}, fmt.Errorf(
				"agy models exited before opening a listen port: %v (stdout: %s; stderr: %s; last lsof error: %v)",
				err, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), lastErr,
			)
		case <-ctx.Done():
			stop()
			return nil, func() {}, fmt.Errorf("wait for temporary agy listen port canceled: %w", ctx.Err())
		case <-timer.C:
			stop()
			return nil, func() {}, fmt.Errorf(
				"timed out waiting for agy models pid %d to listen (stdout: %s; stderr: %s; last lsof error: %v)",
				cmd.Process.Pid, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), lastErr,
			)
		case <-ticker.C:
		}
	}
}

// storeAntigravityUsage records the last successful quota for the fallback path.
func (s *serviceImpl) storeAntigravityUsage(usage *model.AntigravityUsage) {
	if usage == nil {
		return
	}
	cp := *usage
	s.agyMu.Lock()
	s.agyCache = &cp
	s.agyCachedAt = time.Now()
	s.agyMu.Unlock()
}

// cachedAntigravityUsage returns a copy of the last successful quota with any
// window whose reset time has passed dropped, or nil when there is no cache, it
// is older than antigravityCacheMaxAge, or every window has already reset (so the
// stale numbers would mislead rather than help).
func (s *serviceImpl) cachedAntigravityUsage() *model.AntigravityUsage {
	s.agyMu.Lock()
	defer s.agyMu.Unlock()
	if s.agyCache == nil || time.Since(s.agyCachedAt) > antigravityCacheMaxAge {
		return nil
	}

	now := time.Now().Unix()
	cp := *s.agyCache
	cp.Version = ""
	fresh := func(w *model.UsageWindow) *model.UsageWindow {
		if w != nil && w.ResetAt > 0 && w.ResetAt <= now {
			return nil
		}
		return w
	}
	cp.ClaudeWeekly = fresh(cp.ClaudeWeekly)
	cp.Claude5h = fresh(cp.Claude5h)
	cp.GeminiWeekly = fresh(cp.GeminiWeekly)
	cp.Gemini5h = fresh(cp.Gemini5h)

	if cp.ClaudeWeekly == nil && cp.Claude5h == nil && cp.GeminiWeekly == nil && cp.Gemini5h == nil {
		return nil
	}
	return &cp
}

// agyPidLineRe matches a `ps` line's leading "<pid> <first-token> [rest]". Only
// real process lines start with a pid; agy's `-p <prompt>` argument can contain
// newlines, and such wrapped continuation lines don't match, so they're ignored.
var agyPidLineRe = regexp.MustCompile(`^\s*(\d+)\s+(\S+)(.*)$`)

// listenPortRe pulls the port out of an lsof "127.0.0.1:<port>" NAME field.
var listenPortRe = regexp.MustCompile(`127\.0\.0\.1:(\d+)`)

// antigravityExtCSRFFlagRe / antigravityCSRFFlagRe pull CSRF tokens from agy /
// language-server argv. `--csrf_token` must not match as a suffix of
// `--extension_server_csrf_token`, so that flag is anchored at a start/space.
var (
	antigravityExtCSRFFlagRe = regexp.MustCompile(`--extension_server_csrf_token(?:=|[[:space:]]+)(\S+)`)
	antigravityCSRFFlagRe    = regexp.MustCompile(`(?:^|[[:space:]])--csrf_token(?:=|[[:space:]]+)(\S+)`)
	antigravityHTMLCSRFRe    = regexp.MustCompile(`csrfToken"\s*:\s*"([^"]+)"`)
)

type agyProcess struct {
	pid     int
	command string
}

// antigravityEndpoints returns the 127.0.0.1 TCP ports agy processes listen on,
// each tagged with CSRF tokens from that process's command line. Returns an
// empty slice (no error) when no agy process is running — the caller turns that
// into a reported error. Requires ps + lsof (macOS / Linux).
func (s *serviceImpl) antigravityEndpoints(ctx context.Context) ([]antigravityEndpoint, error) {
	procs, err := s.agyProcesses(ctx)
	if err != nil {
		return nil, err
	}
	// Guard: with no pids we must NOT run `lsof -p ""`, which would list every
	// listening port on the host instead of none.
	if len(procs) == 0 {
		return nil, nil
	}
	pids := make([]int, len(procs))
	csrfByPid := make(map[int][]string, len(procs))
	for i, p := range procs {
		pids[i] = p.pid
		csrfByPid[p.pid] = csrfTokensFromCommand(p.command)
	}
	portsByPid, err := s.listenPortsByPid(ctx, pids)
	if err != nil {
		return nil, err
	}
	var endpoints []antigravityEndpoint
	seenPort := map[int]bool{}
	for pid, ports := range portsByPid {
		for _, port := range ports {
			if seenPort[port] {
				continue
			}
			seenPort[port] = true
			endpoints = append(endpoints, antigravityEndpoint{port: port, csrf: csrfByPid[pid]})
		}
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("lsof found no 127.0.0.1 listen port for agy pids %v", pids)
	}
	return endpoints, nil
}

// agyProcesses returns processes whose executable basename is "agy". It reads
// the full command (comm is unreliable across platforms) and matches on the
// basename, so both "agy" and "/path/to/agy --add-dir ..." are recognised while
// "bun .../antigravity-acp" is not.
func (s *serviceImpl) agyProcesses(ctx context.Context) ([]agyProcess, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ps", "-ax", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("run ps failed: %w", err)
	}
	var procs []agyProcess
	seen := map[int]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		m := agyPidLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if filepath.Base(m[2]) != agyBin {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || seen[pid] {
			continue
		}
		seen[pid] = true
		procs = append(procs, agyProcess{pid: pid, command: strings.TrimSpace(m[2] + m[3])})
	}
	return procs, nil
}

// processCSRFTokens reads one pid's command line and extracts CSRF flags. Used
// for the short-lived `agy models` probe, whose argv is not in the earlier
// process snapshot.
func (s *serviceImpl) processCSRFTokens(ctx context.Context, pid int) []string {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return nil
	}
	return csrfTokensFromCommand(strings.TrimSpace(string(out)))
}

func endpointsForPorts(ports []int, csrf []string) []antigravityEndpoint {
	endpoints := make([]antigravityEndpoint, 0, len(ports))
	for _, port := range ports {
		endpoints = append(endpoints, antigravityEndpoint{port: port, csrf: csrf})
	}
	return endpoints
}

func csrfTokensFromCommand(command string) []string {
	var tokens []string
	seen := map[string]bool{}
	add := func(raw string) {
		token := strings.Trim(raw, `"'`)
		if token == "" || seen[token] {
			return
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	// Prefer the HTTP extension-server token: that is the port we POST to.
	if m := antigravityExtCSRFFlagRe.FindStringSubmatch(command); len(m) > 1 {
		add(m[1])
	}
	if m := antigravityCSRFFlagRe.FindStringSubmatch(command); len(m) > 1 {
		add(m[1])
	}
	return tokens
}

// listenPortsForPids returns the distinct 127.0.0.1 listen ports held by the
// given pids. Must only be called with a non-empty pid list.
func (s *serviceImpl) listenPortsForPids(ctx context.Context, pids []int) ([]int, error) {
	portsByPid, err := s.listenPortsByPid(ctx, pids)
	if err != nil {
		return nil, err
	}
	var ports []int
	seen := map[int]bool{}
	for _, pidPorts := range portsByPid {
		for _, port := range pidPorts {
			if seen[port] {
				continue
			}
			seen[port] = true
			ports = append(ports, port)
		}
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("lsof found no 127.0.0.1 listen port for agy pids %v", pids)
	}
	return ports, nil
}

// listenPortsByPid returns the 127.0.0.1 listen ports held by each pid, via a
// single lsof call (pids passed comma-joined to -p). Must only be called with a
// non-empty pid list.
func (s *serviceImpl) listenPortsByPid(ctx context.Context, pids []int) (map[int][]int, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pidArgs := make([]string, len(pids))
	for i, p := range pids {
		pidArgs[i] = strconv.Itoa(p)
	}
	out, err := exec.CommandContext(cctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-a", "-p", strings.Join(pidArgs, ","), "-F", "pn").Output()
	if err != nil {
		// lsof exits non-zero when some pids have no matching FDs; stdout may
		// still hold valid rows, so only surface a non-exit error (e.g. lsof
		// missing / not executable).
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil, fmt.Errorf("run lsof failed: %w", err)
		}
	}
	return parseLsofPN(string(out)), nil
}

// parseLsofPN reads `lsof -F pn` records: a `p<pid>` line followed by
// `n<address>` lines for that process's sockets.
func parseLsofPN(out string) map[int][]int {
	portsByPid := map[int][]int{}
	seen := map[string]bool{}
	pid := 0
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			n, err := strconv.Atoi(line[1:])
			if err != nil {
				pid = 0
				continue
			}
			pid = n
		case 'n':
			if pid == 0 {
				continue
			}
			m := listenPortRe.FindStringSubmatch(line[1:])
			if m == nil {
				continue
			}
			port, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			key := strconv.Itoa(pid) + ":" + strconv.Itoa(port)
			if seen[key] {
				continue
			}
			seen[key] = true
			portsByPid[pid] = append(portsByPid[pid], port)
		}
	}
	return portsByPid
}

// antigravityQuotaResp is the subset of RetrieveUserQuotaSummary we read.
type antigravityQuotaResp struct {
	Response struct {
		Groups []struct {
			Buckets []struct {
				BucketID          string  `json:"bucketId"`
				RemainingFraction float64 `json:"remainingFraction"`
				ResetTime         string  `json:"resetTime"` // RFC3339
			} `json:"buckets"`
		} `json:"groups"`
	} `json:"response"`
}

// antigravityQuota POSTs the quota RPC to one agy port. Current CLI builds
// speak HTTPS on both loopback ports; older builds still have a plaintext
// HTTP port. Try HTTPS first, then HTTP. A leftover mTLS-only port fails
// the handshake and is skipped.
func (s *serviceImpl) antigravityQuota(ctx context.Context, client *http.Client, endpoint antigravityEndpoint) (*model.AntigravityUsage, error) {
	var lastErr, httpsErr error
	for _, scheme := range []string{"https", "http"} {
		usage, err := s.antigravityQuotaOn(ctx, client, scheme, endpoint)
		if err == nil {
			return usage, nil
		}
		lastErr = err
		if scheme == "https" {
			httpsErr = err
		}
	}
	// HTTP-to-HTTPS 400 is noise when the port is TLS-only; keep the HTTPS error.
	if isHTTPOnHTTPS(lastErr) && httpsErr != nil {
		return nil, httpsErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("query antigravity quota on port %d failed", endpoint.port)
}

func (s *serviceImpl) antigravityQuotaOn(ctx context.Context, client *http.Client, scheme string, endpoint antigravityEndpoint) (*model.AntigravityUsage, error) {
	tokens := uniqueNonEmpty(endpoint.csrf)
	var lastErr error
	for _, token := range tokens {
		usage, err := s.postAntigravityQuota(ctx, client, scheme, endpoint.port, token)
		if err == nil {
			return usage, nil
		}
		lastErr = err
		if isHTTPOnHTTPS(err) {
			return nil, err
		}
	}

	if len(tokens) == 0 || isAntigravityMissingCSRF(lastErr) {
		if html := fetchAntigravityHTMLCSRF(ctx, client, scheme, endpoint.port); html != "" && !containsToken(tokens, html) {
			usage, err := s.postAntigravityQuota(ctx, client, scheme, endpoint.port, html)
			if err == nil {
				return usage, nil
			}
			lastErr = err
			tokens = append(tokens, html)
		}
	}

	// Older agy CLI builds answer without a CSRF token.
	if !containsToken(tokens, "") {
		usage, err := s.postAntigravityQuota(ctx, client, scheme, endpoint.port, "")
		if err == nil {
			return usage, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("query antigravity quota on %s://127.0.0.1:%d failed", scheme, endpoint.port)
}

func (s *serviceImpl) postAntigravityQuota(ctx context.Context, client *http.Client, scheme string, port int, csrf string) (*model.AntigravityUsage, error) {
	url := fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, port, antigravityQuotaPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(antigravityQuotaBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if csrf != "" {
		req.Header.Set(antigravityCSRFHeader, csrf)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := readAllLimited(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var r antigravityQuotaResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse quota response failed: %w (body: %s)", err, strings.TrimSpace(string(body)))
	}

	usage := &model.AntigravityUsage{}
	found := false
	for _, g := range r.Response.Groups {
		for _, b := range g.Buckets {
			w := toAntigravityWindow(b.RemainingFraction, b.ResetTime)
			switch b.BucketID {
			case "3p-weekly":
				usage.ClaudeWeekly = w
			case "3p-5h":
				usage.Claude5h = w
			case "gemini-weekly":
				usage.GeminiWeekly = w
			case "gemini-5h":
				usage.Gemini5h = w
			default:
				continue
			}
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("quota response has no known buckets (body: %s)", strings.TrimSpace(string(body)))
	}
	return usage, nil
}

// fetchAntigravityHTMLCSRF reads the CSRF token Antigravity 2.x embeds in the
// HTML (or response header) served at `/`. A 404 / empty body means this port
// is an older tokenless CLI server, not an error.
func fetchAntigravityHTMLCSRF(ctx context.Context, client *http.Client, scheme string, port int) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s://127.0.0.1:%d/", scheme, port), nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if token := strings.TrimSpace(resp.Header.Get(antigravityCSRFHeader)); token != "" {
		return token
	}
	body, err := readAllLimited(resp)
	if err != nil || len(body) == 0 {
		return ""
	}
	m := antigravityHTMLCSRFRe.FindSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return string(m[1])
}

func isAntigravityMissingCSRF(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "missing csrf") || strings.Contains(msg, "csrf token")
}

func isHTTPOnHTTPS(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "HTTP request to an HTTPS server")
}

func uniqueNonEmpty(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func containsToken(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// toAntigravityWindow converts one quota bucket into a UsageWindow. The API
// reports the fraction *remaining* (0..1); the UI ring shows *used* percent, so
// invert it. resetTime is RFC3339 → unix seconds (0 when absent / unparseable).
func toAntigravityWindow(remainingFraction float64, resetTime string) *model.UsageWindow {
	used := (1 - remainingFraction) * 100
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	var resetAt int64
	if resetTime != "" {
		if t, err := time.Parse(time.RFC3339, resetTime); err == nil {
			resetAt = t.Unix()
		}
	}
	return &model.UsageWindow{UsedPercent: used, ResetAt: resetAt}
}
