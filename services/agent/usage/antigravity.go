package usage

import (
	"bytes"
	"context"
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
	// antigravityCacheMaxAge bounds how long a cached quota stays usable when its
	// windows carry no reset time. Mirrors TokenTracker's 7-day cache ceiling.
	antigravityCacheMaxAge = 7 * 24 * time.Hour
	// A freshly started agy opens its ports quickly, but its OAuth-backed quota
	// state takes a few seconds to become available.
	antigravityStartupTimeout = 7 * time.Second
	antigravityRetryInterval  = 250 * time.Millisecond
)

// AntigravityUsage reads the agy plan quota plus the agy CLI version. agy runs as
// a local language server: each agy process listens on a plaintext-HTTP port that
// serves an unauthenticated Connect-RPC quota endpoint (no csrf token needed). We
// resolve those ports via ps + lsof and POST the quota request to each until one
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
	// Otherwise agyPids can observe our own short-lived version process and
	// report it as an agy server with no listening socket.
	ports, portsErr := s.antigravityListenPorts(ctx)
	retryQuota := false
	stopProbe := func() {}
	if portsErr != nil || len(ports) == 0 {
		var probeErr error
		ports, stopProbe, probeErr = s.startAntigravityProbe(ctx)
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
		usage, err = s.antigravityLiveQuota(ctx, ports, retryQuota)
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
func (s *serviceImpl) antigravityLiveQuota(ctx context.Context, ports []int, retry bool) (*model.AntigravityUsage, error) {
	if len(ports) == 0 {
		return nil, fmt.Errorf("no running agy process found (is antigravity active?)")
	}

	// 127.0.0.1 target: never use a proxy. Short timeout since the ports are
	// local and a wrong (HTTPS/mTLS) port fails fast.
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil},
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
	results := make(chan portResult, len(ports))
	for _, port := range ports {
		go func() {
			var lastErr error
			for {
				usage, err := s.antigravityQuota(queryCtx, client, port)
				if err == nil {
					results <- portResult{port: port, usage: usage}
					return
				}
				lastErr = err
				if !retry {
					results <- portResult{port: port, err: lastErr}
					return
				}
				select {
				case <-queryCtx.Done():
					results <- portResult{port: port, err: lastErr}
					return
				case <-time.After(antigravityRetryInterval):
				}
			}
		}()
	}

	var lastErr error
	for range ports {
		result := <-results
		if result.err == nil {
			return result.usage, nil
		}
		lastErr = result.err
		logger.Warnf(ctx, "[agent.usage] antigravity quota on port %d failed: %v", result.port, result.err)
	}
	return nil, fmt.Errorf("query antigravity quota failed on all %d port(s): %w", len(ports), lastErr)
}

// startAntigravityProbe starts a non-generative `agy models` process solely to
// make the local quota RPC available when no prompt-time agy process is alive.
// The returned stop function terminates it after the quota has been read.
func (s *serviceImpl) startAntigravityProbe(ctx context.Context) ([]int, func(), error) {
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
			return ports, stop, nil
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

// agyPidLineRe matches a `ps` line's leading "<pid> <first-token>". Only real
// process lines start with a pid; agy's `-p <prompt>` argument can contain
// newlines, and such wrapped continuation lines don't match, so they're ignored.
var agyPidLineRe = regexp.MustCompile(`^\s*(\d+)\s+(\S+)`)

// listenPortRe pulls the port out of an lsof "127.0.0.1:<port>" NAME field.
var listenPortRe = regexp.MustCompile(`127\.0\.0\.1:(\d+)`)

// antigravityListenPorts returns the 127.0.0.1 TCP ports agy processes listen on.
// It resolves agy pids via `ps`, then their listen ports via a single `lsof`
// call. Returns an empty slice (no error) when no agy process is running — the
// caller turns that into a reported error. Requires ps + lsof (macOS / Linux).
func (s *serviceImpl) antigravityListenPorts(ctx context.Context) ([]int, error) {
	pids, err := s.agyPids(ctx)
	if err != nil {
		return nil, err
	}
	// Guard: with no pids we must NOT run `lsof -p ""`, which would list every
	// listening port on the host instead of none.
	if len(pids) == 0 {
		return nil, nil
	}
	return s.listenPortsForPids(ctx, pids)
}

// agyPids returns the pids whose executable basename is "agy". It reads the full
// command (comm is unreliable across platforms) and matches on the basename, so
// both "agy" and "/path/to/agy --add-dir ..." are recognised while
// "bun .../antigravity-acp" is not.
func (s *serviceImpl) agyPids(ctx context.Context) ([]int, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ps", "-ax", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("run ps failed: %w", err)
	}
	var pids []int
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
		pids = append(pids, pid)
	}
	return pids, nil
}

// listenPortsForPids returns the distinct 127.0.0.1 listen ports held by the
// given pids, via a single lsof call (pids passed comma-joined to -p). Must only
// be called with a non-empty pid list.
func (s *serviceImpl) listenPortsForPids(ctx context.Context, pids []int) ([]int, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pidArgs := make([]string, len(pids))
	for i, p := range pids {
		pidArgs[i] = strconv.Itoa(p)
	}
	out, err := exec.CommandContext(cctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-a", "-p", strings.Join(pidArgs, ",")).Output()
	if err != nil {
		// lsof exits non-zero when some pids have no matching FDs; stdout may
		// still hold valid rows, so only surface a non-exit error (e.g. lsof
		// missing / not executable).
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil, fmt.Errorf("run lsof failed: %w", err)
		}
	}
	var ports []int
	seen := map[int]bool{}
	for _, m := range listenPortRe.FindAllStringSubmatch(string(out), -1) {
		port, err := strconv.Atoi(m[1])
		if err != nil || seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("lsof found no 127.0.0.1 listen port for agy pids %v", pids)
	}
	return ports, nil
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

// antigravityQuota POSTs the quota RPC to one agy port over plaintext HTTP. The
// agy HTTPS port requires mTLS and fails here — that's fine, the caller tries
// every port and keeps the first that answers.
func (s *serviceImpl) antigravityQuota(ctx context.Context, client *http.Client, port int) (*model.AntigravityUsage, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", port, antigravityQuotaPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(antigravityQuotaBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")

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
