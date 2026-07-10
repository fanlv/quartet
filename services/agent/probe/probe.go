// Package probe provides agent discovery and capability caching.
//
// It probes installed ACP agents to discover their models/modes; the cache is
// warmed up at server startup and refreshed lazily on subsequent requests.
package probe

import (
	"context"
	"fmt"
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
	"github.com/fanlv/quartet/services/agent/internal/acpstate"
	"github.com/fanlv/quartet/types/model"
	"golang.org/x/sync/singleflight"
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

// grokIconDataURI is the grok logo (brand orange) as an inline SVG data
// URI, so the icon renders without an external network fetch.
const grokIconDataURI = "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIzNSIgaGVpZ2h0PSIzMyIgdmlld0JveD0iMCAwIDM1IDMzIiBmaWxsPSJub25lIj48cGF0aCBkPSJNMTMuMjM3MSAyMS4wNDA3TDI0LjMxODYgMTIuODUwNkMyNC44NjE5IDEyLjQ0OTEgMjUuNjM4NCAxMi42MDU3IDI1Ljg5NzMgMTMuMjI5NEMyNy4yNTk3IDE2LjUxODUgMjYuNjUxIDIwLjQ3MTIgMjMuOTQwMyAyMy4xODUxQzIxLjIyOTcgMjUuODk4OSAxNy40NTgxIDI2LjQ5NDEgMTQuMDEwOCAyNS4xMzg2TDEwLjI0NDkgMjYuODg0M0MxNS42NDYzIDMwLjU4MDYgMjIuMjA1MyAyOS42NjY1IDI2LjMwNCAyNS41NjAxQzI5LjU1NTEgMjIuMzA1MSAzMC41NjIgMTcuODY4MyAyOS42MjA1IDEzLjg2NzNMMjkuNjI5IDEzLjg3NThDMjguMjYzNyA3Ljk5ODA5IDI5Ljk2NDcgNS42NDg3MSAzMy40NDkgMC44NDQ1NzZDMzMuNTMxNCAwLjczMDY2NyAzMy42MTM5IDAuNjE2NzU3IDMzLjY5NjQgMC41TDI5LjExMTMgNS4wOTA1NVY1LjA3NjMxTDEzLjIzNDMgMjEuMDQzNiIgZmlsbD0iIzAwMDAwMCIvPjxwYXRoIGQ9Ik0xMC45NTAzIDIzLjAzMTNDNy4wNzM0MyAxOS4zMjM1IDcuNzQxODUgMTMuNTg1MyAxMS4wNDk4IDEwLjI3NjNDMTMuNDk1OSA3LjgyNzIyIDE3LjUwMzYgNi44Mjc2NyAyMS4wMDIxIDguMjk3MUwyNC43NTk1IDYuNTU5OThDMjQuMDgyNiA2LjA3MDE3IDIzLjIxNSA1LjU0MzM0IDIyLjIxOTUgNS4xNzMxM0MxNy43MTk4IDMuMzE5MjYgMTIuMzMyNiA0LjI0MTkyIDguNjc0NzkgNy45MDEyNkM1LjE1NjM1IDExLjQyMzkgNC4wNDk5IDE2Ljg0MDMgNS45NDk5MiAyMS40NjIyQzcuMzY5MjQgMjQuOTE2NSA1LjA0MjU3IDI3LjM1OTggMi42OTg4NCAyOS44MjZDMS44NjgyOSAzMC43MDAyIDEuMDM0OSAzMS41NzQ1IDAuMzYzNjQgMzIuNUwxMC45NDc0IDIzLjAzNDEiIGZpbGw9IiMwMDAwMDAiLz48L3N2Zz4K"

// KnownACPAgents lists all known ACP agents.
var KnownACPAgents = []ACPAgentDef{
	{Bin: "traex", Command: "traex acp serve", DisplayName: "TraeCLI", IconURL: "https://avatars.githubusercontent.com/u/192691831"},
	// {Bin: "grok", Command: "grok agent stdio", DisplayName: "Grok", IconURL: grokIconDataURI},
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
// AgentInfo.Type, e.g. "gemini --acp" or "npx @agentclientprotocol/claude-agent-acp")
// to the binary used to run that same tool in *headless one-shot* mode
// (e.g. "gemini", "claude").
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
	models               *model.SessionModelState
	modes                *model.SessionModeState
	thoughtLevelsByModel map[string]*model.SessionThoughtLevelState
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
	acpProbeGroup     singleflight.Group

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

// GetACPSessionInfo returns a cached ACP selector snapshot. A cache miss is
// filled synchronously so /agent/list never returns an installed ACP agent
// without its model/mode selectors merely because startup warmup is still in
// flight. Returned values are deep copies and may be freely mutated by callers.
func GetACPSessionInfo(ctx context.Context, command string) (*model.SessionModelState, *model.SessionModeState, *model.SessionThoughtLevelState, error) {
	if models, modes, thoughtLevels, ok := getCachedACPSessionInfo(command); ok {
		return models, modes, thoughtLevels, nil
	}
	if cooling, wait := recentlyFailed(command); cooling {
		return nil, nil, nil, fmt.Errorf("ACP agent probe is cooling down after a failure: cmd=%s retryAfter=%s", command, wait.Truncate(time.Second))
	}
	if _, err := refreshACPSessionCacheEntry(ctx, command, ""); err != nil {
		return nil, nil, nil, err
	}
	models, modes, thoughtLevels, ok := getCachedACPSessionInfo(command)
	if !ok {
		return nil, nil, nil, fmt.Errorf("ACP agent probe returned no selector state: cmd=%s", command)
	}
	return models, modes, thoughtLevels, nil
}

func getCachedACPSessionInfo(command string) (*model.SessionModelState, *model.SessionModeState, *model.SessionThoughtLevelState, bool) {
	acpSessionCacheMu.RLock()
	defer acpSessionCacheMu.RUnlock()
	cached, ok := acpSessionCache[command]
	if !ok || cached == nil {
		return nil, nil, nil, false
	}
	modelID := currentModelID(cached.models)
	return cloneSessionModelState(cached.models),
		cloneSessionModeState(cached.modes),
		cloneSessionThoughtLevelState(cached.thoughtLevelsByModel[modelID]),
		true
}

// CacheACPConfigState mirrors a successful live-session selector change into
// the probe cache so later /agent/list requests return the most recently used
// values. Model/thought updates are scoped to their model key; mode updates do
// not touch either model-linked field.
func CacheACPConfigState(
	command string,
	target model.ACPConfigTarget,
	modelID string,
	modeID string,
	thoughtLevelID string,
	state *model.ACPConfigState,
) {
	acpSessionCacheMu.Lock()
	defer acpSessionCacheMu.Unlock()
	entry := acpSessionCache[command]
	if entry == nil {
		if state == nil || state.Models == nil {
			return
		}
		entry = &acpSessionInfoCache{thoughtLevelsByModel: make(map[string]*model.SessionThoughtLevelState)}
		acpSessionCache[command] = entry
	}
	if entry.thoughtLevelsByModel == nil {
		entry.thoughtLevelsByModel = make(map[string]*model.SessionThoughtLevelState)
	}
	if state != nil {
		if state.Models != nil {
			entry.models = cloneSessionModelState(state.Models)
		}
		if state.Modes != nil {
			entry.modes = cloneSessionModeState(state.Modes)
		}
	}
	if modelID == "" {
		modelID = currentModelID(entry.models)
	}

	switch target {
	case model.ACPConfigTargetModel:
		if entry.models != nil && modelID != "" {
			entry.models.CurrentModelId = modelID
			var thoughtLevels *model.SessionThoughtLevelState
			if state != nil {
				thoughtLevels = state.ThoughtLevels
			}
			entry.thoughtLevelsByModel[modelID] = cloneSessionThoughtLevelState(thoughtLevels)
		}
	case model.ACPConfigTargetMode:
		if entry.modes != nil {
			entry.modes.CurrentModeId = modeID
		}
	case model.ACPConfigTargetThoughtLevel:
		if entry.models != nil && modelID != "" {
			entry.models.CurrentModelId = modelID
			if state != nil && state.ThoughtLevels != nil {
				entry.thoughtLevelsByModel[modelID] = cloneSessionThoughtLevelState(state.ThoughtLevels)
			}
			if thoughtLevels := entry.thoughtLevelsByModel[modelID]; thoughtLevels != nil {
				thoughtLevels.CurrentThoughtLevelId = thoughtLevelID
			}
		}
	}
}

func cloneACPSessionInfoCache(in *acpSessionInfoCache) *acpSessionInfoCache {
	if in == nil {
		return nil
	}
	out := &acpSessionInfoCache{
		models:               cloneSessionModelState(in.models),
		modes:                cloneSessionModeState(in.modes),
		thoughtLevelsByModel: make(map[string]*model.SessionThoughtLevelState, len(in.thoughtLevelsByModel)),
	}
	for modelID, thoughtLevels := range in.thoughtLevelsByModel {
		out.thoughtLevelsByModel[modelID] = cloneSessionThoughtLevelState(thoughtLevels)
	}
	return out
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
// Uses NewProbeConn (not NewTrackedConn) so the caller's acpProbeTimeout
// deadline bounds the connect + initialize handshake too; NewTrackedConn
// would strip that deadline and widen it back to connCreateTimeout, letting a
// not-logged-in agent hang the probe for up to 60s.
//
// Real packages and paths outside the npx cache are protected by
// looksLikeNpxStaleTemp; see npx_heal.go for the safety contract.
func connectACPWithNpxHealRetry(ctx context.Context, command, cwd string) (*pkgacp.Conn, error) {
	conn, err := pkgacp.NewProbeConn(ctx, command, cwd)
	if err == nil {
		return conn, nil
	}
	cleaned := tryHealNpxENOTEMPTY(err.Error())
	if cleaned == 0 {
		return nil, err
	}
	logger.Infof(ctx, "[probe] healed npx cache: cmd=%s removedTempDirs=%d originalErr=%v (retrying immediately)",
		command, cleaned, err)
	conn, retryErr := pkgacp.NewProbeConn(ctx, command, cwd)
	if retryErr != nil {
		logger.Warnf(ctx, "[probe] retry after npx heal still failed: cmd=%s err=%v", command, retryErr)
		return nil, retryErr
	}
	logger.Infof(ctx, "[probe] connect ACP agent recovered after npx heal: cmd=%s", command)
	return conn, nil
}

// modelsFromSessionResponse extracts the model list from the ACP session
// response. Delegates to the shared acpstate converter so probe and the live
// ACP session agent agree on the mapping.
func modelsFromSessionResponse(resp *pkgacp.SessionResponse) *model.SessionModelState {
	return acpstate.Models(resp)
}

// modesFromSessionResponse extracts the mode list from the ACP session
// response. Delegates to the shared acpstate converter.
func modesFromSessionResponse(resp *pkgacp.SessionResponse) *model.SessionModeState {
	return acpstate.Modes(resp)
}

// thoughtLevelsFromSessionResponse extracts the thought_level list from the
// ACP session response. Delegates to the shared acpstate converter.
func thoughtLevelsFromSessionResponse(resp *pkgacp.SessionResponse) *model.SessionThoughtLevelState {
	return acpstate.ThoughtLevels(resp)
}

// PreviewTarget names which selector a Home (session-less) config switch
// changes.
type PreviewTarget string

const (
	PreviewTargetModel        PreviewTarget = "model"
	PreviewTargetMode         PreviewTarget = "mode"
	PreviewTargetThoughtLevel PreviewTarget = "thoughtLevel"
)

// PreviewSelection carries the full current selection for a Home config
// switch. Target is the selector the user just changed; Model / Mode /
// ThoughtLevel hold the current values of all three so the throwaway session
// can be replayed into the same state before the refreshed lists are read
// back. Empty values are skipped.
type PreviewSelection struct {
	Target       PreviewTarget
	Model        string
	Mode         string
	ThoughtLevel string
}

// PreviewSetConfig applies a session-less selector change against the probe
// cache. Cached state is returned immediately and refreshed asynchronously;
// only an uncached agent/model pair performs a synchronous ACP probe. Because
// each target updates only its own cached selection, refreshing a model-linked
// thought-level list can never reset the cached mode.
func PreviewSetConfig(ctx context.Context, command string, sel PreviewSelection) (*model.ACPConfigState, error) {
	state, cached, err := applyCachedPreviewSelection(command, sel)
	if err != nil {
		return nil, err
	}
	if cached {
		refreshACPModelCacheAsync(ctx, command, selectedPreviewModel(command, sel.Model))
		return state, nil
	}

	if _, err := refreshACPSessionCacheEntry(ctx, command, sel.Model); err != nil {
		return nil, err
	}
	state, cached, err = applyCachedPreviewSelection(command, sel)
	if err != nil {
		return nil, err
	}
	if !cached {
		return nil, fmt.Errorf("ACP selector cache is still missing after synchronous probe: cmd=%s model=%s target=%s", command, sel.Model, sel.Target)
	}
	return state, nil
}

func applyCachedPreviewSelection(command string, sel PreviewSelection) (*model.ACPConfigState, bool, error) {
	acpSessionCacheMu.Lock()
	defer acpSessionCacheMu.Unlock()
	entry, ok := acpSessionCache[command]
	if !ok || entry == nil {
		return nil, false, nil
	}

	modelID := sel.Model
	if modelID == "" {
		modelID = currentModelID(entry.models)
	}
	switch sel.Target {
	case PreviewTargetModel:
		if modelID == "" {
			return nil, false, fmt.Errorf("model is required for agent %s", command)
		}
		if !modelAvailable(entry.models, modelID) {
			return nil, false, fmt.Errorf("set model %q failed: model is not available for agent %s", modelID, command)
		}
		thoughtLevels, known := entry.thoughtLevelsByModel[modelID]
		if !known {
			return nil, false, nil
		}
		entry.models.CurrentModelId = modelID
		return &model.ACPConfigState{
			Models:        cloneSessionModelState(entry.models),
			ThoughtLevels: cloneSessionThoughtLevelState(thoughtLevels),
		}, true, nil

	case PreviewTargetMode:
		if !modeAvailable(entry.modes, sel.Mode) {
			return nil, false, fmt.Errorf("set mode %q failed: mode is not available for agent %s", sel.Mode, command)
		}
		entry.modes.CurrentModeId = sel.Mode
		return &model.ACPConfigState{}, true, nil

	case PreviewTargetThoughtLevel:
		if !modelAvailable(entry.models, modelID) {
			return nil, false, fmt.Errorf("set thought_level %q failed: model %q is not available for agent %s", sel.ThoughtLevel, modelID, command)
		}
		thoughtLevels, known := entry.thoughtLevelsByModel[modelID]
		if !known {
			return nil, false, nil
		}
		if thoughtLevels == nil {
			return nil, false, fmt.Errorf("set thought_level %q failed: agent does not advertise a thought_level config option for model %s", sel.ThoughtLevel, modelID)
		}
		if !thoughtLevelAvailable(thoughtLevels, sel.ThoughtLevel) {
			return nil, false, fmt.Errorf("set thought_level %q failed: value is not available for agent %s model %s", sel.ThoughtLevel, command, modelID)
		}
		entry.models.CurrentModelId = modelID
		thoughtLevels.CurrentThoughtLevelId = sel.ThoughtLevel
		return &model.ACPConfigState{
			Models:        cloneSessionModelState(entry.models),
			ThoughtLevels: cloneSessionThoughtLevelState(thoughtLevels),
		}, true, nil
	}
	return nil, false, fmt.Errorf("invalid preview target %q", sel.Target)
}

func selectedPreviewModel(command, requestedModelID string) string {
	if requestedModelID != "" {
		return requestedModelID
	}
	acpSessionCacheMu.RLock()
	defer acpSessionCacheMu.RUnlock()
	if entry := acpSessionCache[command]; entry != nil {
		return currentModelID(entry.models)
	}
	return ""
}

func refreshACPModelCacheAsync(ctx context.Context, command, modelID string) {
	bgCtx := context.WithoutCancel(ctx)
	safe.Go(bgCtx, func() {
		if cooling, wait := recentlyFailed(command); cooling {
			logger.Debugf(bgCtx, "[probe] skip ACP model refresh in cooldown: cmd=%s model=%s waitRemaining=%s", command, modelID, wait.Truncate(time.Second))
			return
		}
		if _, err := refreshACPSessionCacheEntry(bgCtx, command, modelID); err != nil {
			logger.Debugf(bgCtx, "[probe] async ACP model refresh failed: cmd=%s model=%s err=%v", command, modelID, err)
		}
	})
}

func fetchACPSessionInfoForAgent(ctx context.Context, command, preferredModelID string) (*acpSessionInfoCache, error) {
	// Bound every probe so one slow / hung agent can't wedge the refresh
	// goroutine or a synchronous cache miss.
	ctx, cancel := context.WithTimeout(ctx, acpProbeTimeout)
	defer cancel()

	var cwd string
	if home, err := fileserver.UserHomeDir(); err == nil {
		cwd = home
	}
	acpConn, err := connectACPWithNpxHealRetry(ctx, command, cwd)
	if err != nil {
		err = fmt.Errorf("connect ACP agent failed: cmd=%s: %w", command, err)
		recordProbeFailure(ctx, command, err)
		return nil, err
	}
	defer acpConn.Close()

	// Probe sessions are cheap; create a fresh session each time to avoid
	// persisting/maintaining probe-session IDs across refresh cycles.
	sessResp, err := acpConn.NewSession(ctx, cwd)
	if err != nil {
		err = fmt.Errorf("create ACP session failed: cmd=%s: %w", command, err)
		recordProbeFailure(ctx, command, err)
		return nil, err
	}
	if recovered := noteProbeSuccess(command); recovered > 0 {
		logger.Infof(ctx, "[probe] ACP agent recovered: cmd=%s consecutiveFailures=%d", command, recovered)
	}

	models := modelsFromSessionResponse(sessResp)
	modes := modesFromSessionResponse(sessResp)
	defaultModelID := currentModelID(models)
	thoughtLevelsByModel := map[string]*model.SessionThoughtLevelState{}
	if defaultModelID != "" {
		// Keep the key even when the value is nil. Presence means this model was
		// probed and is known not to expose a thought-level option.
		thoughtLevelsByModel[defaultModelID] = thoughtLevelsFromSessionResponse(sessResp)
	}

	if preferredModelID != "" && preferredModelID != defaultModelID {
		if !modelAvailable(models, preferredModelID) {
			return nil, fmt.Errorf("set model %q failed: model is not available for agent %s", preferredModelID, command)
		}
		linkedResp, setErr := acpConn.SetSessionModel(ctx, pkgacp.SessionID(sessResp.SessionID), preferredModelID)
		if setErr != nil {
			err = fmt.Errorf("set model %q failed: %w", preferredModelID, setErr)
			recordProbeFailure(ctx, command, err)
			return nil, err
		}
		if linkedModels := modelsFromSessionResponse(linkedResp); linkedModels != nil {
			models = linkedModels
		} else if models != nil {
			models.CurrentModelId = preferredModelID
		}
		thoughtLevelsByModel[preferredModelID] = thoughtLevelsFromSessionResponse(linkedResp)
	}

	// Preserve the historical initial-mode policy, but only when creating the
	// cache entry. Later refreshes merge the user's cached current mode back in.
	if modes != nil {
		if id := PickDefaultModeID(modes.AvailableModes); id != "" {
			modes.CurrentModeId = id
		}
	}
	thoughtLevels := thoughtLevelsByModel[currentModelID(models)]
	logger.Infof(ctx, "[probe] ACP session info loaded: cmd=%s model=%q models=%d mode=%q modes=%d thoughtLevel=%q thoughtLevels=%d",
		command,
		currentModelID(models), countModels(models),
		currentModeID(modes), countModes(modes),
		currentThoughtLevelID(thoughtLevels), countThoughtLevels(thoughtLevels))
	logger.Infof(ctx, "[probe] %s ACP session info: %v", command, json.String(sessResp))

	return &acpSessionInfoCache{
		models:               models,
		modes:                modes,
		thoughtLevelsByModel: thoughtLevelsByModel,
	}, nil
}

func recordProbeFailure(ctx context.Context, command string, err error) {
	shouldLog, suppressed, firstFailedAt := noteProbeFailure(command, err)
	if shouldLog {
		logger.Warnf(ctx, "[probe] ACP agent probe failed: cmd=%s err=%v suppressed=%d firstFailedAgo=%s",
			command, err, suppressed, time.Since(firstFailedAt).Truncate(time.Second))
	}
}

func modelAvailable(models *model.SessionModelState, modelID string) bool {
	if models == nil || modelID == "" {
		return false
	}
	for _, available := range models.AvailableModels {
		if available.ModelId == modelID {
			return true
		}
	}
	return false
}

func modeAvailable(modes *model.SessionModeState, modeID string) bool {
	if modes == nil || modeID == "" {
		return false
	}
	for _, available := range modes.AvailableModes {
		if available.Id == modeID {
			return true
		}
	}
	return false
}

func thoughtLevelAvailable(state *model.SessionThoughtLevelState, thoughtLevelID string) bool {
	if state == nil || thoughtLevelID == "" {
		return false
	}
	for _, available := range state.AvailableThoughtLevels {
		if available.Id == thoughtLevelID {
			return true
		}
	}
	return false
}

func mergeACPSessionInfoCache(previous, fresh *acpSessionInfoCache) *acpSessionInfoCache {
	if fresh == nil {
		return cloneACPSessionInfoCache(previous)
	}
	merged := cloneACPSessionInfoCache(fresh)
	if previous == nil {
		return merged
	}

	if previous.models != nil && modelAvailable(merged.models, previous.models.CurrentModelId) {
		merged.models.CurrentModelId = previous.models.CurrentModelId
	}
	if previous.modes != nil && modeAvailable(merged.modes, previous.modes.CurrentModeId) {
		merged.modes.CurrentModeId = previous.modes.CurrentModeId
	}
	if merged.thoughtLevelsByModel == nil {
		merged.thoughtLevelsByModel = make(map[string]*model.SessionThoughtLevelState)
	}
	for modelID, previousState := range previous.thoughtLevelsByModel {
		freshState, refreshed := merged.thoughtLevelsByModel[modelID]
		if !refreshed {
			merged.thoughtLevelsByModel[modelID] = cloneSessionThoughtLevelState(previousState)
			continue
		}
		if previousState != nil && freshState != nil && thoughtLevelAvailable(freshState, previousState.CurrentThoughtLevelId) {
			freshState.CurrentThoughtLevelId = previousState.CurrentThoughtLevelId
		}
	}
	return merged
}

func refreshACPSessionCacheEntry(ctx context.Context, command, preferredModelID string) (*acpSessionInfoCache, error) {
	key := command + "\x00" + preferredModelID
	value, err, _ := acpProbeGroup.Do(key, func() (any, error) {
		fresh, fetchErr := fetchACPSessionInfoForAgent(ctx, command, preferredModelID)
		if fetchErr != nil {
			return nil, fetchErr
		}
		acpSessionCacheMu.Lock()
		merged := mergeACPSessionInfoCache(acpSessionCache[command], fresh)
		acpSessionCache[command] = merged
		stored := cloneACPSessionInfoCache(merged)
		acpSessionCacheMu.Unlock()
		return stored, nil
	})
	if err != nil {
		return nil, err
	}
	entry, ok := value.(*acpSessionInfoCache)
	if !ok || entry == nil {
		return nil, fmt.Errorf("ACP agent probe returned invalid cache state: cmd=%s model=%s", command, preferredModelID)
	}
	return entry, nil
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
	targets := InstalledACPAgents()
	var wg sync.WaitGroup
	skippedCooldown := 0
	// Bound the number of in-flight probes so 10+ npx/node cold starts
	// can't all kick off in the same second on AgentList refresh. The
	// cap is read here (not at package init) so tests / operators can
	// override QUARTET_ACP_PROBE_CONCURRENCY without re-importing.
	probeSlots := make(chan struct{}, acpProbeConcurrency())
	for _, target := range targets {
		command := target.Command
		acpSessionCacheMu.RLock()
		prev := acpSessionCache[command]
		preferredModelID := ""
		if prev != nil {
			preferredModelID = currentModelID(prev.models)
		}
		acpSessionCacheMu.RUnlock()

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
			if _, err := refreshACPSessionCacheEntry(ctx, command, preferredModelID); err != nil {
				logger.Debugf(ctx, "[probe] refresh ACP cache entry failed: cmd=%s model=%s err=%v", command, preferredModelID, err)
			}
		})
	}
	wg.Wait()
	if skippedCooldown > 0 {
		logger.Debugf(ctx, "[probe] refresh complete: probed=%d skippedCooldown=%d", len(targets)-skippedCooldown, skippedCooldown)
	}

	// Drop entries for commands that are no longer installed without replacing
	// the whole map; replacing it would race explicit model/mode selections made
	// while this background refresh was in flight.
	installed := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		installed[target.Command] = struct{}{}
	}
	acpSessionCacheMu.Lock()
	for command := range acpSessionCache {
		if _, ok := installed[command]; !ok {
			delete(acpSessionCache, command)
		}
	}
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
