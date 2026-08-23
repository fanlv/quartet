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

	requireSourceMatch(t, source, `if let workspaceID, !workspaces\.contains[\s\S]*await cacheStore\.clear\(\)[\s\S]*failureWorkspaceID = nil[\s\S]*visibleJobsResponse = try await client\.jobs\(\s*workspaceID: nil,\s*limit: 100,\s*excludeScheduled: excludeScheduled\s*\)[\s\S]*isCurrentDashboardRequest\(generation: generation, workspaceID: nil\)`, "a deleted persisted workspace must fall back to the all-workspace first page without dropping the scheduled-job filter")
	requireSourceMatch(t, source, `catch \{\s*guard isCurrentDashboardRequest\(generation: generation, workspaceID: failureWorkspaceID\)`, "fallback errors must be attributed to the corrected workspace selection")
}

func TestAuthenticatedConnectionKeepsDashboardAvailableOnInitialRefreshFailure(t *testing.T) {
	source := iosSource(t, "Quartet/App/AppModel.swift")

	requireSourceMatch(t, source, `presentFailure: true,\s*disconnectOnFailure: false`, "connect must present the full first-refresh error without treating it as an authentication failure")
	requireSourceMatch(t, source, `if disconnect \{\s*phase = \.disconnected\s*\} else \{\s*phase = \.connected`, "a dashboard-only failure after authentication must preserve the validated connection")
	requireSourceMatch(t, source, `if presentToUser \{\s*present\(error\)`, "dashboard failures must retain the complete error detail for the copyable error sheet")
	requireSourceMatch(t, source, `guard phase != \.connecting \|\| presentFailure else \{ return \}`, "a silent view refresh must not supersede the mandatory post-authentication refresh")
	requireSourceMatch(t, source, `failureMessage = "\\\(apiError\.summary\)\\n\\n\\\(apiError\.detail\)"`, "connection state must retain the full API error detail")
}

func TestUserLoginUsesCookiesWithoutPersistingPassword(t *testing.T) {
	source := iosSource(t, "Quartet/App/AppModel.swift")
	client := iosSource(t, "Quartet/Core/Networking/APIClient.swift")

	requireSourceMatch(t, source, `@Published var username: String`, "connection form must accept a username")
	requireSourceMatch(t, source, `@Published var password: String`, "connection form must accept a password")
	requireSourceMatch(t, source, `principal = try await client\.login\(username: requestedUsername, password: password\)`, "connection must establish a user session when no cookie is available")
	requireSourceMatch(t, source, `csrfToken = principal\.csrfToken[\s\S]*password = ""`, "a successful login must retain CSRF state and discard the password")
	requireSourceMatch(t, source, `private var connectionGeneration: UInt64`, "superseded connection attempts must be invalidated")
	requireSourceMatch(t, source, `guard isCurrentConnectionRequest\([\s\S]*requestedUsername: requestedUsername`, "an old login response must not commit state for a new address or username")
	if strings.Contains(source, "KeychainStore.write") { t.Fatal("password or session must not be written to Keychain by AppModel") }
	if strings.Contains(client, "X-AGENT-AUTH") { t.Fatal("iOS client must not send the removed shared-token header") }
	requireSourceMatch(t, client, `X-CSRF-Token`, "mutating requests must carry the session CSRF token")
}

func TestDashboardCacheCannotChangeTheAuthoritativeServerOrigin(t *testing.T) {
	source := iosSource(t, "Quartet/App/AppModel.swift")
	start := strings.Index(source, "private func loadCachedDashboardIfNeeded() async")
	end := strings.Index(source, "private func markSyncSucceeded()")
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
