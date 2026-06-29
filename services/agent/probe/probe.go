// Package probe provides agent discovery and capability caching.
//
// It probes installed ACP agents to discover their models/modes; the cache is
// warmed up at server startup and refreshed lazily on subsequent requests.
package probe

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/json"

	"github.com/fanlv/quartet/pkg/fileserver"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/types/model"
)

// acpProbeTimeout bounds a single ACP agent probe (connect + new session)
// so a hanging agent binary can't block the async refresh goroutine
// indefinitely. Sized generously because cold-start for node-based agents
// (claude, codex, opencode, qwen) can be slow on first run.
const acpProbeTimeout = 30 * time.Second

// acpProbeFailureBackoff is the cooldown a command sits out after failing
// to probe. Without it, every AgentList HTTP call triggers a fresh
// RefreshACPSessionCacheAsync that re-attempts every installed agent —
// for a persistently broken one (npx cache corrupted, agent binary
// missing a runtime dep, ...) that produces a steady stream of WARN logs
// proportional to UI poll frequency. The backoff is reset on the next
// successful probe.
const acpProbeFailureBackoff = 5 * time.Minute

// acpProbeFailureLogEvery suppresses repeated identical failure WARNs
// while the backoff window is active: log the first failure, then every
// Nth one inside the cooldown. The skipped count is included in the
// next emitted line so nothing is silently lost.
const acpProbeFailureLogEvery = 10

// acpProbeConcurrencyEnv overrides the cap on simultaneous in-flight ACP
// probes during a single refresh. KnownACPAgents currently lists 13
// entries, every one of them potentially a node-based subprocess with a
// slow cold-start; fan-out without a bound spikes CPU/memory/fd usage
// on a developer laptop right when AgentList is also rendering. The
// default below trades a small refresh-completion delay for steady
// resource usage.
const acpProbeConcurrencyEnv = "QUARTET_ACP_PROBE_CONCURRENCY"

// defaultACPProbeConcurrency caps how many probes run in parallel when
// the env var is unset or invalid. 4 is small enough to keep load
// predictable on a single-user laptop yet large enough that a 13-agent
// install still completes in well under the 30s per-probe timeout.
const defaultACPProbeConcurrency = 4

// acpProbeConcurrency reads acpProbeConcurrencyEnv and falls back to
// defaultACPProbeConcurrency for empty / non-numeric / non-positive
// values. Read on each refresh (rather than cached) so operators can
// retune via env var without restarting.
func acpProbeConcurrency() int {
	raw := os.Getenv(acpProbeConcurrencyEnv)
	if raw == "" {
		return defaultACPProbeConcurrency
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return defaultACPProbeConcurrency
	}
	return v
}

// ACPAgentDef describes a known ACP agent binary.
type ACPAgentDef struct {
	Bin         string // binary looked up in $PATH
	Command     string // full command stored in AgentInfo.Type
	DisplayName string
	IconURL     string
}

// KnownACPAgents lists all known ACP agents.
var KnownACPAgents = []ACPAgentDef{
	{"coco", "coco acp serve", "COCO", "🥥"},
	{Bin: "traex", Command: "traex acp serve", DisplayName: "TraeCLI", IconURL: "https://avatars.githubusercontent.com/u/192691831"},
	{"openclaw", "openclaw acp", "OpenClaw", "🦞"},
	{"claude", "npx @agentclientprotocol/claude-agent-acp", "Claude", "https://avatars.githubusercontent.com/u/81847"},
	{"gemini", "gemini --acp", "Gemini", "https://avatars.githubusercontent.com/u/161781182"},
	{"cursor-agent", "cursor-agent acp", "Cursor", "https://avatars.githubusercontent.com/u/126759922"},
	{"copilot", "copilot --acp --stdio", "Copilot", "🧑‍✈️"},
	{"droid", "droid exec --output-format acp", "Droid", "https://avatars.githubusercontent.com/u/131064358"},
	{"kimi", "kimi acp", "Kimi", "https://avatars.githubusercontent.com/u/129152888"},
	{"codex", "npx @agentclientprotocol/codex-acp", "Codex", "https://avatars.githubusercontent.com/u/14957082"},
	{"kiro-cli", "kiro-cli acp", "Kiro", "https://avatars.githubusercontent.com/u/207925904"},
	{"opencode", "opencode acp", "OpenCode", "https://avatars.githubusercontent.com/in/1549082"},
	{"kilocode", "npx -y @kilocode/cli acp", "KiloCode", "https://avatars.githubusercontent.com/u/201822503"},
	{"qwen", "qwen --acp", "Qwen", "https://avatars.githubusercontent.com/u/141221163"},
}

// InitAllowedAgentCommands pushes KnownACPAgents' command strings to
// pkg/acp's execution allowlist. Must be called once at startup
// (cmd/web/main.go) before any ACP subprocess is launched — NewConn
// rejects commands that are not registered.
//
// Made explicit (rather than an init()) so startup ordering is visible
// in the entrypoint and tests wanting to exercise a subset of commands
// can call pkgacp.RegisterAllowedAgentCommands directly without import
// ordering tricks.
func InitAllowedAgentCommands() {
	commands := make([]string, 0, len(KnownACPAgents))
	for _, a := range KnownACPAgents {
		commands = append(commands, a.Command)
	}
	pkgacp.RegisterAllowedAgentCommands(commands)
}

// InstalledACPAgents returns the subset of KnownACPAgents whose binary is in $PATH.
func InstalledACPAgents() []ACPAgentDef {
	var installed []ACPAgentDef
	for _, a := range KnownACPAgents {
		if _, err := exec.LookPath(a.Bin); err == nil {
			installed = append(installed, a)
		}
	}
	return installed
}

// HeadlessBin maps an ACP agent's *serve* command (the string stored in
// AgentInfo.Type, e.g. "coco acp serve" or "npx @agentclientprotocol/claude-agent-acp")
// to the binary used to run that same tool in *headless one-shot* mode
// (e.g. "coco", "claude").
//
// This distinction matters: an ACP serve command speaks JSON-RPC over
// stdio and cannot be invoked as `<cmd> -p <prompt>`; doing so just boots
// the ACP server and exits non-zero. One-shot text flows (title / IM
// reply) must instead run the agent's plain CLI in print mode, which is
// exactly what KnownACPAgents already records as Bin. The second return
// value reports whether command was a recognised ACP agent.
func HeadlessBin(command string) (string, bool) {
	for _, a := range KnownACPAgents {
		if a.Command == command {
			return a.Bin, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// ACP session info cache
// ---------------------------------------------------------------------------

type acpSessionInfoCache struct {
	models        *model.SessionModelState
	modes         *model.SessionModeState
	thoughtLevels *model.SessionThoughtLevelState
}

// acpProbeFailureState tracks consecutive failures of a single agent
// command so RefreshACPSessionCacheAsync can short-circuit while the
// backoff window is open, and so identical repeated failures fold into
// summary lines instead of one WARN per HTTP poll.
type acpProbeFailureState struct {
	firstFailedAt time.Time
	lastFailedAt  time.Time
	count         int // consecutive failures since the last success
	suppressed    int // failures swallowed since the last emitted line
	lastErr       string
}

var (
	acpSessionCache   = make(map[string]*acpSessionInfoCache)
	acpSessionCacheMu sync.RWMutex
	acpRefreshing     atomic.Bool

	acpProbeFailures   = make(map[string]*acpProbeFailureState)
	acpProbeFailuresMu sync.Mutex
)

// recentlyFailed reports whether the given command failed within the
// backoff window and the caller should skip re-probing it. Returns the
// remaining wait time for log context.
func recentlyFailed(command string) (bool, time.Duration) {
	acpProbeFailuresMu.Lock()
	defer acpProbeFailuresMu.Unlock()
	st, ok := acpProbeFailures[command]
	if !ok {
		return false, 0
	}
	wait := acpProbeFailureBackoff - time.Since(st.lastFailedAt)
	if wait <= 0 {
		return false, 0
	}
	return true, wait
}

// noteProbeFailure records a probe failure and decides whether the
// caller should emit a WARN. shouldLog is true on the 1st failure and
// every Nth thereafter; when shouldLog is true, suppressed is the
// number of identical failures that were folded into this line and
// firstFailedAt is the start of the failure run.
func noteProbeFailure(command string, err error) (shouldLog bool, suppressed int, firstFailedAt time.Time) {
	acpProbeFailuresMu.Lock()
	defer acpProbeFailuresMu.Unlock()
	now := time.Now()
	st, ok := acpProbeFailures[command]
	if !ok {
		st = &acpProbeFailureState{firstFailedAt: now}
		acpProbeFailures[command] = st
	}
	st.lastFailedAt = now
	st.count++
	if err != nil {
		st.lastErr = err.Error()
	}
	if st.count == 1 || st.count%acpProbeFailureLogEvery == 0 {
		suppressed = st.suppressed
		st.suppressed = 0
		return true, suppressed, st.firstFailedAt
	}
	st.suppressed++
	return false, 0, st.firstFailedAt
}

// noteProbeSuccess clears any failure state for the command and returns
// the count of consecutive failures the caller should mention in a
// recovery INFO line (zero if there was no prior failure).
func noteProbeSuccess(command string) int {
	acpProbeFailuresMu.Lock()
	defer acpProbeFailuresMu.Unlock()
	if st, ok := acpProbeFailures[command]; ok {
		recovered := st.count
		delete(acpProbeFailures, command)
		return recovered
	}
	return 0
}

// GetACPSessionInfo returns the cached models/modes for an ACP agent command.
//
// The returned values are deep copies of the cache entries so callers can
// freely mutate them (AgentList writes a default CurrentModeId into Modes,
// for example) without racing other concurrent /agent/list handlers on the
// same cached objects. Without copying, the cached pointers would be
// shared write targets across all goroutines and corrupt the cache too.
func GetACPSessionInfo(command string) (*model.SessionModelState, *model.SessionModeState, *model.SessionThoughtLevelState) {
	acpSessionCacheMu.RLock()
	defer acpSessionCacheMu.RUnlock()
	if cached, ok := acpSessionCache[command]; ok {
		return cloneSessionModelState(cached.models), cloneSessionModeState(cached.modes), cloneSessionThoughtLevelState(cached.thoughtLevels)
	}
	return nil, nil, nil
}

func cloneSessionModelState(in *model.SessionModelState) *model.SessionModelState {
	if in == nil {
		return nil
	}
	out := &model.SessionModelState{CurrentModelId: in.CurrentModelId}
	if len(in.AvailableModels) > 0 {
		out.AvailableModels = make([]model.ModelInfoACP, len(in.AvailableModels))
		copy(out.AvailableModels, in.AvailableModels)
		for i := range out.AvailableModels {
			if d := out.AvailableModels[i].Description; d != nil {
				v := *d
				out.AvailableModels[i].Description = &v
			}
		}
	}
	return out
}

func cloneSessionModeState(in *model.SessionModeState) *model.SessionModeState {
	if in == nil {
		return nil
	}
	out := &model.SessionModeState{CurrentModeId: in.CurrentModeId}
	if len(in.AvailableModes) > 0 {
		out.AvailableModes = make([]model.ACPSessionMode, len(in.AvailableModes))
		copy(out.AvailableModes, in.AvailableModes)
		for i := range out.AvailableModes {
			if d := out.AvailableModes[i].Description; d != nil {
				v := *d
				out.AvailableModes[i].Description = &v
			}
		}
	}
	return out
}

func cloneSessionThoughtLevelState(in *model.SessionThoughtLevelState) *model.SessionThoughtLevelState {
	if in == nil {
		return nil
	}
	out := &model.SessionThoughtLevelState{
		CurrentThoughtLevelId: in.CurrentThoughtLevelId,
		ConfigId:              in.ConfigId,
	}
	if len(in.AvailableThoughtLevels) > 0 {
		out.AvailableThoughtLevels = make([]model.ACPThoughtLevel, len(in.AvailableThoughtLevels))
		copy(out.AvailableThoughtLevels, in.AvailableThoughtLevels)
		for i := range out.AvailableThoughtLevels {
			if d := out.AvailableThoughtLevels[i].Description; d != nil {
				v := *d
				out.AvailableThoughtLevels[i].Description = &v
			}
		}
	}
	return out
}

// connectACPWithNpxHealRetry connects to an ACP agent. On a stale npx
// _npx cache ENOTEMPTY failure, it removes the offending temp dirs in
// place and retries the connection ONCE before giving up — without this
// retry, the failed-probe backoff (acpProbeFailureBackoff, 5 min) gates
// the next attempt, so recovery from a transient cache-corruption is
// delayed by minutes instead of completing in the same call.
//
// Real packages and paths outside the npx cache are protected by
// looksLikeNpxStaleTemp; see npx_heal.go for the safety contract.
func connectACPWithNpxHealRetry(ctx context.Context, command, cwd string) (*pkgacp.Conn, error) {
	conn, err := pkgacp.NewTrackedConn(ctx, command, cwd)
	if err == nil {
		return conn, nil
	}
	cleaned := tryHealNpxENOTEMPTY(err.Error())
	if cleaned == 0 {
		return nil, err
	}
	logger.Infof(ctx, "[probe] healed npx cache: cmd=%s removedTempDirs=%d originalErr=%v (retrying immediately)",
		command, cleaned, err)
	conn, retryErr := pkgacp.NewTrackedConn(ctx, command, cwd)
	if retryErr != nil {
		logger.Warnf(ctx, "[probe] retry after npx heal still failed: cmd=%s err=%v", command, retryErr)
		return nil, retryErr
	}
	logger.Infof(ctx, "[probe] connect ACP agent recovered after npx heal: cmd=%s", command)
	return conn, nil
}

// modelsFromSessionResponse extracts the model list from the ACP session
// response. The ACP v1 schema dropped the dedicated Models field, so models
// now always come from the "model" ConfigOptions select (index 1 by convention
// in agents like claude-agent-acp that don't set the category).
func modelsFromSessionResponse(resp *pkgacp.SessionResponse) *model.SessionModelState {
	if resp == nil {
		return nil
	}
	selectOpt := resp.ModelConfigSelect()
	if selectOpt == nil {
		return nil
	}
	ms := &model.SessionModelState{CurrentModelId: selectOpt.CurrentValue}
	for _, o := range selectOpt.Options {
		var desc *string
		if o.Description != "" {
			d := o.Description
			desc = &d
		}
		ms.AvailableModels = append(ms.AvailableModels, model.ModelInfoACP{
			Description: desc,
			ModelId:     o.Value,
			Name:        o.Name,
		})
	}
	return ms
}

// modesFromSessionResponse extracts the mode list from the ACP session
// response. It prefers the standard Modes field; when that is nil/empty it
// falls back to the "mode" ConfigOptions select (agents like opencode expose
// modes there instead of populating Modes).
func modesFromSessionResponse(resp *pkgacp.SessionResponse) *model.SessionModeState {
	if resp == nil {
		return nil
	}
	// Prefer the standard Modes field.
	if resp.Modes != nil {
		ms := &model.SessionModeState{
			CurrentModeId: string(resp.Modes.CurrentModeID),
		}
		for _, m := range resp.Modes.AvailableModes {
			var desc *string
			if m.Description != "" {
				d := m.Description
				desc = &d
			}
			ms.AvailableModes = append(ms.AvailableModes, model.ACPSessionMode{
				Description: desc,
				Id:          string(m.ID),
				Name:        m.Name,
			})
		}
		return ms
	}
	selectOpt := resp.ModeConfigSelect()
	if selectOpt == nil {
		return nil
	}
	ms := &model.SessionModeState{CurrentModeId: selectOpt.CurrentValue}
	for _, o := range selectOpt.Options {
		var desc *string
		if o.Description != "" {
			d := o.Description
			desc = &d
		}
		ms.AvailableModes = append(ms.AvailableModes, model.ACPSessionMode{
			Description: desc,
			Id:          o.Value,
			Name:        o.Name,
		})
	}
	return ms
}

// thoughtLevelsFromSessionResponse extracts the thought_level list from the
// ACP session response. thought_level has no standard top-level field, so it
// is sourced solely from the "thought_level" ConfigOptions select. The
// select's ConfigID is carried through so the setter can target the right
// config option (e.g. "reasoning_effort").
func thoughtLevelsFromSessionResponse(resp *pkgacp.SessionResponse) *model.SessionThoughtLevelState {
	if resp == nil {
		return nil
	}
	selectOpt := resp.ThoughtLevelConfigSelect()
	if selectOpt == nil {
		return nil
	}
	ts := &model.SessionThoughtLevelState{
		CurrentThoughtLevelId: selectOpt.CurrentValue,
		ConfigId:              selectOpt.ConfigID,
	}
	for _, o := range selectOpt.Options {
		var desc *string
		if o.Description != "" {
			d := o.Description
			desc = &d
		}
		ts.AvailableThoughtLevels = append(ts.AvailableThoughtLevels, model.ACPThoughtLevel{
			Description: desc,
			Id:          o.Value,
			Name:        o.Name,
		})
	}
	return ts
}

func fetchACPSessionInfoForAgent(ctx context.Context, command string) (*model.SessionModelState, *model.SessionModeState, *model.SessionThoughtLevelState) {
	// Bound every probe so one slow / hung agent can't wedge the refresh
	// goroutine. Callers that want no timeout should not exist — the cache
	// refresh is best-effort and missing entries degrade gracefully.
	ctx, cancel := context.WithTimeout(ctx, acpProbeTimeout)
	defer cancel()

	var cwd string
	if home, err := fileserver.UserHomeDir(); err == nil {
		cwd = home
	}
	acpConn, err := connectACPWithNpxHealRetry(ctx, command, cwd)
	if err != nil {
		shouldLog, suppressed, firstFailedAt := noteProbeFailure(command, err)
		if shouldLog {
			logger.Warnf(ctx, "[probe] connect ACP agent failed: cmd=%s err=%v suppressed=%d firstFailedAgo=%s",
				command, err, suppressed, time.Since(firstFailedAt).Truncate(time.Second))
		}
		return nil, nil, nil
	}
	defer acpConn.Close()

	// Probe sessions are cheap; create a fresh session each time to avoid
	// persisting/maintaining probe-session IDs across refresh cycles.
	sessResp, err := acpConn.NewSession(ctx, cwd)
	if err != nil {
		shouldLog, suppressed, firstFailedAt := noteProbeFailure(command, err)
		if shouldLog {
			logger.Warnf(ctx, "[probe] create ACP session failed: cmd=%s err=%v suppressed=%d firstFailedAgo=%s",
				command, err, suppressed, time.Since(firstFailedAt).Truncate(time.Second))
		}
		return nil, nil, nil
	}
	if recovered := noteProbeSuccess(command); recovered > 0 {
		logger.Infof(ctx, "[probe] ACP agent recovered: cmd=%s consecutiveFailures=%d", command, recovered)
	}

	models := modelsFromSessionResponse(sessResp)
	modes := modesFromSessionResponse(sessResp)
	thoughtLevels := thoughtLevelsFromSessionResponse(sessResp)
	logger.Infof(ctx, "[probe] ACP session info loaded: cmd=%s model=%q models=%d mode=%q modes=%d thoughtLevel=%q thoughtLevels=%d",
		command,
		currentModelID(models), countModels(models),
		currentModeID(modes), countModes(modes),
		currentThoughtLevelID(thoughtLevels), countThoughtLevels(thoughtLevels))
	logger.Debugf(ctx, "[probe] %s ACP session info: %v", command, json.String(sessResp))

	return models, modes, thoughtLevels
}

func currentModelID(models *model.SessionModelState) string {
	if models == nil {
		return ""
	}
	return models.CurrentModelId
}

func countModels(models *model.SessionModelState) int {
	if models == nil {
		return 0
	}
	return len(models.AvailableModels)
}

func currentModeID(modes *model.SessionModeState) string {
	if modes == nil {
		return ""
	}
	return modes.CurrentModeId
}

func countModes(modes *model.SessionModeState) int {
	if modes == nil {
		return 0
	}
	return len(modes.AvailableModes)
}

func currentThoughtLevelID(thoughtLevels *model.SessionThoughtLevelState) string {
	if thoughtLevels == nil {
		return ""
	}
	return thoughtLevels.CurrentThoughtLevelId
}

func countThoughtLevels(thoughtLevels *model.SessionThoughtLevelState) int {
	if thoughtLevels == nil {
		return 0
	}
	return len(thoughtLevels.AvailableThoughtLevels)
}

func refreshACPSessionCache(ctx context.Context) {
	type result struct {
		command string
		info    *acpSessionInfoCache
	}

	targets := InstalledACPAgents()

	results := make([]result, len(targets))
	var wg sync.WaitGroup
	skippedCooldown := 0
	// Bound the number of in-flight probes so 10+ npx/node cold starts
	// can't all kick off in the same second on AgentList refresh. The
	// cap is read here (not at package init) so tests / operators can
	// override QUARTET_ACP_PROBE_CONCURRENCY without re-importing.
	probeSlots := make(chan struct{}, acpProbeConcurrency())
	for i, t := range targets {
		idx, command := i, t.Command
		// Pre-fill the slot with the existing cache entry under this command.
		// Two reasons:
		//   1. If the goroutine returns early (ctx cancel before acquiring a
		//      probe slot), results[idx] would otherwise stay at zero value
		//      ({command:"", info:nil}) and the rebuild loop would write
		//      next[""]=nil — polluting the cache with an empty key AND
		//      losing the previously-good entry under `command`.
		//   2. The cooldown branch wants the same carry-forward semantics, so
		//      lifting it out of that branch keeps both paths consistent.
		acpSessionCacheMu.RLock()
		prev := acpSessionCache[command]
		acpSessionCacheMu.RUnlock()
		results[idx] = result{command: command, info: prev}

		// Carry forward the existing cache entry for commands still in
		// their failure-backoff window. Without this, "recently failed"
		// would also mean "wiped from the cache and refetched on the
		// next miss", which defeats the backoff.
		if cooling, wait := recentlyFailed(command); cooling {
			skippedCooldown++
			logger.Debugf(ctx, "[probe] skip ACP probe in cooldown: cmd=%s waitRemaining=%s", command, wait.Truncate(time.Second))
			continue
		}
		wg.Add(1)
		safe.Go(ctx, func() {
			defer wg.Done()
			// Acquire a slot before doing any subprocess work. The
			// channel acts as a counting semaphore: at most
			// acpProbeConcurrency() probes hold a slot at once; the
			// rest park here until one releases.
			//
			// ctx-aware acquire: WarmupACPSessionCache documents that the
			// caller's ctx gates the work so server shutdown stops new
			// subprocess probes. A bare `probeSlots <- struct{}{}` blocked
			// on shutdown — a parked goroutine eventually got a slot and
			// fired a fresh probe long after ctx was cancelled, holding
			// acpRefreshing true and delaying wg.Wait(). RefreshACPSessionCacheAsync
			// uses WithoutCancel so its goroutines are immune to caller
			// cancellation by design; only warmup-style root ctx
			// cancellation is meant to short-circuit here.
			select {
			case probeSlots <- struct{}{}:
			case <-ctx.Done():
				logger.Debugf(ctx, "[probe] skip ACP probe: ctx canceled before acquiring slot, cmd=%s", command)
				return
			}
			defer func() { <-probeSlots }()
			models, modes, thoughtLevels := fetchACPSessionInfoForAgent(ctx, command)
			logger.Debugf(ctx, "[probe] %s ACP session models: %v", command, json.String(models))
			logger.Debugf(ctx, "[probe] %s ACP session modes: %v", command, json.String(modes))
			logger.Debugf(ctx, "[probe] %s ACP session thoughtLevels: %v", command, json.String(thoughtLevels))
			results[idx] = result{command: command, info: &acpSessionInfoCache{models: models, modes: modes, thoughtLevels: thoughtLevels}}
		})
	}
	wg.Wait()
	if skippedCooldown > 0 {
		logger.Debugf(ctx, "[probe] refresh complete: probed=%d skippedCooldown=%d", len(targets)-skippedCooldown, skippedCooldown)
	}

	// Rebuild the cache from scratch so entries for agents that are no
	// longer installed are dropped. Without this the map grows over the
	// lifetime of the process as operators install + uninstall ACP agents.
	next := make(map[string]*acpSessionInfoCache, len(results))
	for _, r := range results {
		if r.command == "" {
			// Defensive: results[idx] is pre-filled before launching each
			// goroutine, so this should be unreachable. Skip rather than
			// trust the invariant — writing next[""]=nil silently corrupts
			// the cache.
			continue
		}
		next[r.command] = r.info
	}
	acpSessionCacheMu.Lock()
	acpSessionCache = next
	acpSessionCacheMu.Unlock()
}

// WarmupACPSessionCache starts an async background refresh of the ACP session
// cache. The caller's ctx gates the work so server shutdown during warmup stops
// new subprocess probes from firing — previously this goroutine hardcoded
// context.Background() and kept spawning during shutdown.
//
// Shares the acpRefreshing gate with RefreshACPSessionCacheAsync: a warmup
// already in flight when the first HTTP-triggered refresh arrives would
// otherwise double the subprocess-probe fan-out in the startup window.
func WarmupACPSessionCache(ctx context.Context) {
	safe.Go(ctx, func() {
		if err := ctx.Err(); err != nil {
			logger.Debugf(ctx, "[probe] warmup skipped: ctx already canceled: %v", err)
			return
		}
		if !acpRefreshing.CompareAndSwap(false, true) {
			logger.Debugf(ctx, "[probe] warmup skipped: refresh already in flight")
			return
		}
		defer acpRefreshing.Store(false)
		logger.Debugf(ctx, "[probe] warming ACP session cache")
		refreshACPSessionCache(ctx)
		logger.Infof(ctx, "[probe] ACP session cache ready")
	})
}

// RefreshACPSessionCacheAsync triggers an async refresh if one is not already
// in progress. The caller's ctx is typically a short-lived request ctx, which
// would cancel the refresh as soon as the HTTP handler returns; wrap with
// WithoutCancel so logger/trace attrs propagate but cancellation does not.
func RefreshACPSessionCacheAsync(ctx context.Context) {
	if !acpRefreshing.CompareAndSwap(false, true) {
		return
	}
	bgCtx := context.WithoutCancel(ctx)
	safe.Go(bgCtx, func() {
		defer acpRefreshing.Store(false)
		refreshACPSessionCache(bgCtx)
	})
}

// PickDefaultModeID selects a default mode from the available modes list,
// preferring modes with "all", "full", or "bypass" in their ID.
func PickDefaultModeID(modes []model.ACPSessionMode) string {
	for _, m := range modes {
		if matchesWholeWord(m.Id, "all") || matchesWholeWord(m.Id, "full") || matchesWholeWord(m.Id, "bypass") {
			return m.Id
		}
	}
	return ""
}

// matchesWholeWord checks if s contains word as a whole word (delimited by
// non-alphanumeric characters or string boundaries), case-insensitively.
func matchesWholeWord(s, word string) bool {
	lower := strings.ToLower(s)
	wl := len(word)
	for i := 0; i+wl <= len(lower); i++ {
		if lower[i:i+wl] != word {
			continue
		}
		leftOK := i == 0 || !isAlnum(lower[i-1])
		rightOK := i+wl == len(lower) || !isAlnum(lower[i+wl])
		if leftOK && rightOK {
			return true
		}
	}
	return false
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// ---------------------------------------------------------------------------
// Eino workspace directory
// ---------------------------------------------------------------------------

// EinoWorkdir returns the local home dir used as the eino workspace.
// Previously this fetched the remote sandbox's workspace via HTTP; now that
// the eino agent writes to the local filesystem, we just resolve $HOME.
func EinoWorkdir() string {
	home, err := fileserver.UserHomeDir()
	if err != nil {
		logger.Errorf(context.Background(), "[probe] UserHomeDir failed: %v", err)
		return ""
	}
	return home
}
