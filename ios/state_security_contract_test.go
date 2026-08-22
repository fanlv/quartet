package ios

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func iosSource(t *testing.T, relativePath string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate iOS contract test")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), relativePath))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	return string(data)
}

func requireSourceMatch(t *testing.T, source, expression, message string) {
	t.Helper()
	if !regexp.MustCompile(expression).MatchString(source) {
		t.Fatalf("%s; missing source contract %q", message, expression)
	}
}

func TestDashboardCacheNeverPersistsShareToken(t *testing.T) {
	source := iosSource(t, "Quartet/Core/Persistence/DashboardCacheStore.swift")
	start := strings.Index(source, "struct CachedJobSummary")
	end := strings.Index(source, "actor DashboardCacheStore")
	if start < 0 || end <= start {
		t.Fatal("cannot locate CachedJobSummary source block")
	}
	cachedType := source[start:end]

	if regexp.MustCompile(`(?m)^\s*let shareToken:`).MatchString(cachedType) {
		t.Fatal("CachedJobSummary must not encode shareToken")
	}
	if strings.Contains(cachedType, "job.shareToken") {
		t.Fatal("CachedJobSummary must not copy shareToken from an API response")
	}
	requireSourceMatch(t, cachedType, `shareToken:\s*nil`, "rehydrated cached jobs must not expose a share token")
	requireSourceMatch(t, cachedType, `agentId = job\.agentId[\s\S]*acpMode = job\.acpMode[\s\S]*acpThoughtLevel = job\.acpThoughtLevel`, "non-sensitive persisted job configuration must survive dashboard caching")
	requireSourceMatch(t, source, `removeItem\(at: cacheURL\)[\s\S]*write\(snapshot\)`, "legacy cache files must be removed before a sanitized snapshot is rewritten")
	requireSourceMatch(t, source, `decodedSnapshot\.jobs\.filter \{ \$0\.workspaceId == selectedWorkspaceID \}`, "legacy cache rows must be reconciled with their selected workspace")
	requireSourceMatch(t, source, `advanceGeneration\(to generation: UInt64`, "cache writes need a generation barrier")
	requireSourceMatch(t, source, `guard generation >= minimumWritableGeneration else \{ return \}`, "stale cache writes must be rejected")
}

func TestDashboardRefreshAndPaginationHaveGenerationGuards(t *testing.T) {
	source := iosSource(t, "Quartet/App/AppModel.swift")

	requireSourceMatch(t, source, `private var dashboardGeneration: UInt64`, "dashboard state needs a monotonic generation")
	requireSourceMatch(t, source, `private var dashboardRefreshTask: Task<Void, Never>\?`, "superseded dashboard refreshes must be cancellable")
	requireSourceMatch(t, source, `private var loadMoreTask: Task<Void, Never>\?`, "superseded pagination must be cancellable")
	requireSourceMatch(t, source, `dashboardRefreshTask\?\.cancel\(\)[\s\S]*loadMoreTask\?\.cancel\(\)`, "a new generation must cancel both refresh and pagination tasks")
	requireSourceMatch(t, source, `guard isCurrentDashboardRequest\(generation: generation, workspaceID: workspaceID\) else \{ return \}`, "dashboard responses must validate generation and workspace before commit")
	requireSourceMatch(t, source, `guard isCurrentPageRequest\(\s*generation: generation,\s*workspaceID: workspaceID,\s*cursor: cursor\s*\) else \{ return \}`, "page responses must validate generation, workspace, and cursor before commit")
	requireSourceMatch(t, source, `cacheStore\.save\([\s\S]*generation: generation`, "cache saves must carry the request generation")
}

func TestWorkspaceSelectionInvalidatesVisiblePageBeforeRefreshing(t *testing.T) {
	source := iosSource(t, "Quartet/App/AppModel.swift")
	start := strings.Index(source, "func selectWorkspace(_ id: String?) async")
	end := strings.Index(source, "func reloadJobs() async")
	if start < 0 || end <= start {
		t.Fatal("cannot locate selectWorkspace source block")
	}
	selection := source[start:end]

	requireSourceMatch(t, selection, `jobs = \[\][\s\S]*nextCursor = nil[\s\S]*hasMoreJobs = false`, "switching workspaces must invalidate the previous visible page and cursor immediately")
	requireSourceMatch(t, selection, `refreshDashboard\(userInitiated: false, clearCachedSnapshot: true\)`, "switching workspaces must invalidate the old on-disk snapshot before loading the new filter")
}

func TestMissingPersistedWorkspaceFallsBackWithinTheMandatoryRefresh(t *testing.T) {
	source := iosSource(t, "Quartet/App/AppModel.swift")

	requireSourceMatch(t, source, `if let workspaceID, !workspaces\.contains[\s\S]*await cacheStore\.clear\(\)[\s\S]*failureWorkspaceID = nil[\s\S]*visibleJobsResponse = try await client\.jobs\(workspaceID: nil, limit: 100\)[\s\S]*isCurrentDashboardRequest\(generation: generation, workspaceID: nil\)`, "a deleted persisted workspace must fall back to an unfiltered first page inside the mandatory refresh")
	requireSourceMatch(t, source, `catch \{\s*guard isCurrentDashboardRequest\(generation: generation, workspaceID: failureWorkspaceID\)`, "fallback errors must be attributed to the corrected workspace selection")
}

func TestAuthenticatedConnectionKeepsDashboardAvailableOnInitialRefreshFailure(t *testing.T) {
	source := iosSource(t, "Quartet/App/AppModel.swift")

	requireSourceMatch(t, source, `presentFailure: true,\s*disconnectOnFailure: false`, "connect must present the full first-refresh error without treating it as an authentication failure")
	requireSourceMatch(t, source, `if disconnect \{\s*phase = \.disconnected\s*\} else \{\s*phase = \.connected`, "a dashboard-only failure after authentication must preserve the validated connection")
	requireSourceMatch(t, source, `if presentToUser \{\s*present\(error\)`, "dashboard failures must retain the complete error detail for the copyable error sheet")
	requireSourceMatch(t, source, `guard phase != \.connecting \|\| presentFailure else \{ return \}`, "a silent view refresh must not supersede the mandatory post-authentication refresh")
	requireSourceMatch(t, source, `failureMessage = "\\\(apiError\.summary\)\\n\\n\\\(apiError\.detail\)"`, "connection state must retain the full API error detail")
	requireSourceMatch(t, source, `: "同步失败，请在应用内查看完整错误。"`, "local notification text must not include the full server response")
}

func TestCredentialsAreScopedToServerOrigin(t *testing.T) {
	source := iosSource(t, "Quartet/App/AppModel.swift")

	requireSourceMatch(t, source, `loadStoredToken\(for: storedServerAddress, migrateLegacyCredential: true\)`, "startup must load only the token for the persisted server origin")
	requireSourceMatch(t, source, `tokenAccount\(for: client\.baseURL\.absoluteString\)`, "a successful connection must write the token under the normalized destination origin")
	requireSourceMatch(t, source, `func editConnection\(\)[\s\S]*KeychainStore\.delete\(account: StorageKey\.tokenAccount\(for: credentialServerAddress\)\)[\s\S]*token = ""`, "reconfiguring the server must delete and discard the old origin token")
	requireSourceMatch(t, source, `didSet \{\s*guard StorageKey\.connectionIdentity\(for: serverAddress\) != StorageKey\.connectionIdentity\(for: oldValue\)[\s\S]*invalidateDashboardRequests\(\)[\s\S]*token = ""`, "editing a server address must invalidate dashboard work and discard a token from another origin")
	requireSourceMatch(t, source, `private var connectionGeneration: UInt64`, "superseded connection attempts must be invalidated")
	requireSourceMatch(t, source, `guard isCurrentConnectionRequest\([\s\S]*requestedToken: requestedToken[\s\S]*else \{ return \}`, "an old connection response must not commit credentials or state for a new address")
	requireSourceMatch(t, source, `finishSupersededConnectionIfNeeded\(generation: generation\)[\s\S]*if generation == connectionGeneration, phase == \.connecting \{\s*phase = \.disconnected`, "editing only the token during validation must return the UI to a retryable state")
	requireSourceMatch(t, source, `migrateLegacyCredential: true[\s\S]*KeychainStore\.delete\(account: StorageKey\.legacyTokenAccount\)`, "the legacy global keychain entry must migrate only to the previously persisted server")
	requireSourceMatch(t, source, `agent-auth-token\|\\\(connectionIdentity\(for: serverAddress\) \?\? "invalid-server"\)`, "keychain account identity must include the complete normalized server base URL")
}

func TestDashboardCacheCannotChangeTheAuthoritativeServerOrigin(t *testing.T) {
	source := iosSource(t, "Quartet/App/AppModel.swift")
	start := strings.Index(source, "private func loadCachedDashboardIfNeeded() async")
	end := strings.Index(source, "private func refreshNotificationAuthorization() async")
	if start < 0 || end <= start {
		t.Fatal("cannot locate cached dashboard loader")
	}
	loader := source[start:end]

	requireSourceMatch(t, loader, `guard StorageKey\.connectionIdentity\(for: snapshot\.serverAddress\) == StorageKey\.connectionIdentity\(for: serverAddress\) else \{\s*await cacheStore\.clear\(\)`, "a cache from another normalized server base URL must be rejected and cleared")
	requireSourceMatch(t, loader, `guard snapshot\.selectedWorkspaceID == selectedWorkspaceID else \{\s*await cacheStore\.clear\(\)`, "a cache from an obsolete workspace selection must be rejected and cleared")
	requireSourceMatch(t, loader, `guard snapshot\.credentialNamespace == credentialCacheNamespace else \{\s*await cacheStore\.clear\(\)`, "a cache from another credential generation must be rejected and cleared")
	if strings.Contains(loader, "serverAddress = snapshot.serverAddress") {
		t.Fatal("dashboard cache must never overwrite the authoritative connection address")
	}
	requireSourceMatch(t, source, `if hasValidatedConnection \{\s*await loadCachedDashboardIfNeeded\(\)\s*\} else \{\s*await cacheStore\.clear\(\)`, "an explicitly unvalidated connection must never restore a stale dashboard")
}
