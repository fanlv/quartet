package job

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/msgextra"
)

func TestParseControlFile_Empty(t *testing.T) {
	f := writeTempFile(t, "")
	vars, stopLoop, stopWorkflow := parseControlFile(context.Background(), fileserver.GetFileManager(), "test-job", f)
	if vars != nil || stopLoop || stopWorkflow {
		t.Errorf("empty file should return nil/false, got vars=%v stopLoop=%v stopWorkflow=%v", vars, stopLoop, stopWorkflow)
	}
}

func TestParseControlFile_StopLoop(t *testing.T) {
	f := writeTempFile(t, "STOP_LOOP\n")
	_, stopLoop, stopWorkflow := parseControlFile(context.Background(), fileserver.GetFileManager(), "test-job", f)
	if !stopLoop {
		t.Error("should detect STOP_LOOP")
	}
	if stopWorkflow {
		t.Error("should not detect STOP_WORKFLOW")
	}
}

func TestParseControlFile_StopWorkflow(t *testing.T) {
	f := writeTempFile(t, "STOP_WORKFLOW\n")
	_, stopLoop, stopWorkflow := parseControlFile(context.Background(), fileserver.GetFileManager(), "test-job", f)
	if stopLoop {
		t.Error("should not detect STOP_LOOP")
	}
	if !stopWorkflow {
		t.Error("should detect STOP_WORKFLOW")
	}
}

func TestParseControlFile_PlainVars(t *testing.T) {
	f := writeTempFile(t, "key1=value1\nkey2=value2\n")
	vars, _, _ := parseControlFile(context.Background(), fileserver.GetFileManager(), "test-job", f)
	if vars["key1"] != "value1" || vars["key2"] != "value2" {
		t.Errorf("unexpected vars: %v", vars)
	}
}

func TestParseControlFile_Base64Vars(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello=world"))
	f := writeTempFile(t, "B64:mykey="+encoded+"\n")
	vars, _, _ := parseControlFile(context.Background(), fileserver.GetFileManager(), "test-job", f)
	if vars["mykey"] != "hello=world" {
		t.Errorf("expected 'hello=world', got %q", vars["mykey"])
	}
}

func TestParseControlFile_Mixed(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("encoded_val"))
	content := "plain=abc\nB64:b64key=" + encoded + "\nSTOP_LOOP\n"
	f := writeTempFile(t, content)
	vars, stopLoop, stopWorkflow := parseControlFile(context.Background(), fileserver.GetFileManager(), "test-job", f)
	if vars["plain"] != "abc" {
		t.Errorf("plain key: got %q", vars["plain"])
	}
	if vars["b64key"] != "encoded_val" {
		t.Errorf("b64 key: got %q", vars["b64key"])
	}
	if !stopLoop {
		t.Error("should detect STOP_LOOP")
	}
	if stopWorkflow {
		t.Error("should not detect STOP_WORKFLOW")
	}
}

func TestParseControlFile_NonExistent(t *testing.T) {
	vars, stopLoop, stopWorkflow := parseControlFile(context.Background(), fileserver.GetFileManager(), "test-job", "/tmp/nonexistent-quartet-test-file")
	if vars != nil || stopLoop || stopWorkflow {
		t.Error("non-existent file should return nil/false")
	}
}

func TestBuildShellEnv_DefaultPassthroughWithSensitiveDenylist(t *testing.T) {
	t.Setenv(envShellPassthrough, "")
	got := toEnvMap(buildShellEnv([]string{
		"PATH=/usr/bin",
		"HOME=/home/quartet",
		"TERM=xterm-256color",
		"LC_ALL=C.UTF-8",
		"GOPATH=/home/quartet/go",
		"NVM_DIR=/home/quartet/.nvm",
		"HTTP_PROXY=http://proxy",
		"LOCAL_MEMORY=/home/quartet/memory",
		"KEYBOARD_LAYOUT=us",
		"CUSTOM=1",
		"OPENAI_API_KEY=secret",
		"DEEPSEEK_KEY=secret",
		"MISTRAL_KEY=secret",
		"GEMINI_KEY=secret",
		"GOOGLE_APPLICATION_CREDENTIALS=/tmp/creds.json",
		"DASHSCOPE_API_KEY=secret",
		"VOLC_ACCESSKEY=secret",
		"BYTEPLUS_TOKEN=secret",
		"AWS_PROFILE=dev",
		"ODD_SECRET_NAME=still-secret",
		"PUBLIC_KEY=public-but-key-like",
		"QUARTET_CONTROL=stale",
	}))
	if got["PATH"] != "/usr/bin" || got["HOME"] != "/home/quartet" || got["TERM"] != "xterm-256color" || got["LC_ALL"] != "C.UTF-8" || got["GOPATH"] != "/home/quartet/go" || got["NVM_DIR"] != "/home/quartet/.nvm" || got["HTTP_PROXY"] != "http://proxy" || got["LOCAL_MEMORY"] != "/home/quartet/memory" || got["KEYBOARD_LAYOUT"] != "us" || got["CUSTOM"] != "1" {
		t.Fatalf("expected non-sensitive envs to pass through: %#v", got)
	}
	// OPENAI_API_KEY is on the default passthrough allowlist
	// (shellEnvDefaultPassthrough): Quartet runs single-user and may use an
	// OpenAI-compatible backend itself, so shell tasks calling the same service
	// must see the key. It passes through despite matching the OPENAI_ /
	// _API_KEY sensitive rules.
	if got["OPENAI_API_KEY"] != "secret" {
		t.Fatalf("OPENAI_API_KEY should pass through (default allowlist): %#v", got)
	}
	for _, key := range []string{
		"DEEPSEEK_KEY",
		"MISTRAL_KEY",
		"GEMINI_KEY",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"DASHSCOPE_API_KEY",
		"VOLC_ACCESSKEY",
		"BYTEPLUS_TOKEN",
		"PUBLIC_KEY",
	} {
		if _, ok := got[key]; ok {
			t.Fatalf("sensitive env %s should not pass through: %#v", key, got)
		}
	}
	if _, ok := got["AWS_PROFILE"]; ok {
		t.Fatalf("AWS env should not pass through by default: %#v", got)
	}
	if _, ok := got["ODD_SECRET_NAME"]; ok {
		t.Fatalf("secret-like env should not pass through: %#v", got)
	}
	if _, ok := got["QUARTET_CONTROL"]; ok {
		t.Fatalf("reserved control env must never be inherited: %#v", got)
	}
}

func TestBuildShellEnv_Passthrough(t *testing.T) {
	t.Setenv(envShellPassthrough, " openai_api_key , AWS_PROFILE , QUARTET_CONTROL ")
	got := toEnvMap(buildShellEnv([]string{
		"PATH=/usr/bin",
		"OPENAI_API_KEY=secret",
		"AWS_PROFILE=dev",
		"QUARTET_CONTROL=stale",
	}))
	if got["OPENAI_API_KEY"] != "secret" || got["AWS_PROFILE"] != "dev" {
		t.Fatalf("passthrough env missing: %#v", got)
	}
	if _, ok := got["QUARTET_CONTROL"]; ok {
		t.Fatalf("reserved control env must never be inherited: %#v", got)
	}
}

func TestShellHelpersSyntax(t *testing.T) {
	script := shellHelpers + "\ntrue\n"
	cmd := exec.Command("bash", "-n", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shellHelpers has syntax error: %v\n%s", err, out)
	}
}

func TestTempFilesUseDedicatedShellDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	baseDir, err := shellTempBaseDir()
	if err != nil {
		t.Fatalf("shellTempBaseDir failed: %v", err)
	}
	wantDir, err := shellTempDir()
	if err != nil {
		t.Fatalf("shellTempDir failed: %v", err)
	}
	expected := filepath.Join(baseDir, ".quartet-shell-"+shellInstanceID)
	if wantDir != expected {
		t.Fatalf("shellTempDir = %q, want instance-scoped dir %q", wantDir, expected)
	}
	// Sanity-check the embedded identifier format: "<startNanos>-<pid>".
	startNanos, pid, legacy, ok := parseShellTempDirName(".quartet-shell-" + shellInstanceID)
	if !ok || legacy || startNanos <= 0 || pid != os.Getpid() {
		t.Fatalf("parseShellTempDirName(current) = (%d,%d,legacy=%v,ok=%v), want (>0,%d,false,true)",
			startNanos, pid, legacy, ok, os.Getpid())
	}

	fm := fileserver.GetFileManager()
	scriptPath, cleanupScript, err := writeShellTempFile(fm, "", "echo hi")
	if err != nil {
		t.Fatalf("writeShellTempFile failed: %v", err)
	}
	defer cleanupScript()
	if filepath.Dir(scriptPath) != wantDir {
		t.Fatalf("script temp dir = %q, want %q", filepath.Dir(scriptPath), wantDir)
	}

	ctrlPath, cleanupCtrl, err := createControlFile(fm, "")
	if err != nil {
		t.Fatalf("createControlFile failed: %v", err)
	}
	defer cleanupCtrl()
	if filepath.Dir(ctrlPath) != wantDir {
		t.Fatalf("control temp dir = %q, want %q", filepath.Dir(ctrlPath), wantDir)
	}
}

func TestCleanupResidualTempFiles_OnlyTouchesDedicatedDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	baseDir, err := shellTempBaseDir()
	if err != nil {
		t.Fatalf("shellTempBaseDir failed: %v", err)
	}
	fm := fileserver.GetFileManager()
	currentDir, err := ensureShellTempDir(fm)
	if err != nil {
		t.Fatalf("ensureShellTempDir failed: %v", err)
	}
	stalePID := os.Getpid() + 100000
	for processExists(stalePID) {
		stalePID++
	}
	// Use the new instance-scoped format for the stale dir so we exercise
	// the PID-collision scenario: same PID as current, different startNanos.
	// (An old legacy-format dir is covered by a separate test below.)
	staleInstanceID := fmt.Sprintf("%d-%d", time.Now().UnixNano()-1_000_000_000, os.Getpid())
	staleDir := filepath.Join(baseDir, ".quartet-shell-"+staleInstanceID)
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatalf("mkdir stale dir failed: %v", err)
	}
	// Dir owned by a dead unrelated PID (non-collision path).
	deadPIDDir := filepath.Join(baseDir, ".quartet-shell-"+fmt.Sprintf("%d-%d", time.Now().UnixNano()-2_000_000_000, stalePID))
	if err := os.MkdirAll(deadPIDDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-pid dir failed: %v", err)
	}
	legacyShell := filepath.Join(baseDir, ".quartet-shell-legacy.sh")
	currentShell := filepath.Join(currentDir, ".quartet-shell-live.sh")
	staleShell := filepath.Join(staleDir, ".quartet-shell-leftover.sh")
	staleCtrl := filepath.Join(staleDir, ".quartet-ctrl-leftover.txt")

	if err := os.MkdirAll(currentDir, 0o755); err != nil {
		t.Fatalf("mkdir current dir failed: %v", err)
	}

	for _, path := range []string{legacyShell, currentShell, staleShell, staleCtrl} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write temp file %q failed: %v", path, err)
		}
	}

	cleanupResidualTempFiles(fm, nil)

	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale PID-collision dir should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(deadPIDDir); !os.IsNotExist(err) {
		t.Fatalf("stale dead-pid dir should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(currentShell); err != nil {
		t.Fatalf("current instance temp file should be untouched, stat err=%v", err)
	}
	if _, err := os.Stat(legacyShell); err != nil {
		t.Fatalf("legacy top-level temp file should be untouched, stat err=%v", err)
	}
}

func TestCleanupResidualTempFiles_LegacyDirIsDeleted(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_MEMORY", root)
	baseDir, err := shellTempBaseDir()
	if err != nil {
		t.Fatalf("shellTempBaseDir failed: %v", err)
	}
	// Legacy-format dir (".quartet-shell-<pid>") from a pre-fix quartet
	// version. We deliberately use the current PID to simulate the Docker
	// restart scenario: the old instance crashed and the new one has the
	// same PID, but the legacy dir is never owned by the new instance
	// (which now writes the instance-scoped format).
	legacyDir := filepath.Join(baseDir, ".quartet-shell-"+strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy dir failed: %v", err)
	}
	fm := fileserver.GetFileManager()
	currentDir, err := ensureShellTempDir(fm)
	if err != nil {
		t.Fatalf("ensureShellTempDir failed: %v", err)
	}
	currentShell := filepath.Join(currentDir, ".quartet-shell-live.sh")
	if err := os.WriteFile(currentShell, []byte("x"), 0o600); err != nil {
		t.Fatalf("write current temp file failed: %v", err)
	}

	cleanupResidualTempFiles(fm, nil)

	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy-format dir (Docker PID=1 restart scenario) should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(currentShell); err != nil {
		t.Fatalf("current instance temp file should be untouched, stat err=%v", err)
	}
}

// TestCleanupResidualTempFiles_WorkdirRemovesOrphans pins the M1 fix: shell
// steps that wrote .quartet-shell-*.sh / .quartet-ctrl-*.txt directly
// into a workspace workdir and then crashed before defer-cleanup must have
// those orphans reaped on the next quartet boot. Live files (within the
// staleWorkdirTempAge window) and unrelated user files must be left alone.
func TestCleanupResidualTempFiles_WorkdirRemovesOrphans(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	workdir := t.TempDir()

	staleShell := filepath.Join(workdir, ".quartet-shell-orphan.sh")
	staleCtrl := filepath.Join(workdir, ".quartet-ctrl-orphan.txt")
	freshShell := filepath.Join(workdir, ".quartet-shell-fresh.sh")
	userFile := filepath.Join(workdir, "main.go")

	for _, p := range []string{staleShell, staleCtrl, freshShell, userFile} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %q failed: %v", p, err)
		}
	}
	// Backdate the orphans past the cleanup threshold; freshShell stays
	// at "now" so the cleanup must skip it.
	old := time.Now().Add(-staleWorkdirTempAge - time.Hour)
	for _, p := range []string{staleShell, staleCtrl} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("backdate %q failed: %v", p, err)
		}
	}

	cleanupResidualTempFiles(fileserver.GetFileManager(), []string{workdir})

	if _, err := os.Stat(staleShell); !os.IsNotExist(err) {
		t.Fatalf("stale .quartet-shell-*.sh should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(staleCtrl); !os.IsNotExist(err) {
		t.Fatalf("stale .quartet-ctrl-*.txt should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(freshShell); err != nil {
		t.Fatalf("fresh .quartet-shell-*.sh should be kept (within window), stat err=%v", err)
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Fatalf("unrelated user file must never be touched, stat err=%v", err)
	}
}

func TestExecuteShellRepeat_PersistsInterruptedShellOutput(t *testing.T) {
	memoryRoot := t.TempDir()
	t.Setenv("LOCAL_MEMORY", memoryRoot)

	svc := newStateTestService()
	job := &model.Job{
		ID:          "job-shell-interrupted",
		WorkspaceID: "ws-shell-interrupted",
		Workdir:     t.TempDir(),
		Progress:    &model.JobProgress{},
	}
	node := model.FlowNode{
		Type:        model.FlowNodeTypeStep,
		RoundType:   model.RoundTypeShell,
		Message:     "printf 'before-cancel\\n'; while true; do sleep 0.05; done",
		AgentType:   "shell",
		StepModelID: "",
	}

	reader, err := svc.Subscribe(job.ID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	ctx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	resultCh := make(chan stepResult, 1)
	go func() {
		resultCh <- svc.executeShellRepeat(ctx, job, stubRunner{}, node, []int{0}, "sess-shell", nil)
	}()

	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	seenOutput := false
	for !seenOutput {
		entries, ok := reader.Read(readCtx, 16)
		if !ok {
			t.Fatal("timeout waiting for shell output before cancel")
		}
		for _, entry := range entries {
			if delta, ok := entry.Event.(*model.TextMessageContentEvent); ok && strings.Contains(delta.Delta, "before-cancel") {
				seenOutput = true
				cancelRun()
				break
			}
			if entry.Seq > 0 {
				reader.Ack(entry.Seq)
			}
		}
	}

	select {
	case got := <-resultCh:
		if got != stepAborted {
			t.Fatalf("executeShellRepeat result=%v, want %v", got, stepAborted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for executeShellRepeat to abort")
	}

	repo, err := repository.NewChatContextRepo(job.WorkspaceID, job.ID, "sess-shell")
	if err != nil {
		t.Fatalf("NewChatContextRepo failed: %v", err)
	}
	messages, err := repo.LoadAllMessages(context.Background())
	if err != nil {
		t.Fatalf("LoadAllMessages failed: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("persisted messages=%d, want 2", len(messages))
	}

	userMsg := messages[0]
	assistantMsg := messages[1]
	if userMsg.Content != node.Message {
		t.Fatalf("user message content mismatch: got %q", userMsg.Content)
	}
	if got, _ := userMsg.Extra[msgextra.KeyShellOutput].(bool); !got {
		t.Fatalf("user message missing shellOutput extra: %#v", userMsg.Extra)
	}
	if assistantMsg.Content == "" || !strings.Contains(assistantMsg.Content, "before-cancel") {
		t.Fatalf("assistant message missing interrupted output: %q", assistantMsg.Content)
	}
	if got, _ := assistantMsg.Extra[msgextra.KeyShellOutput].(bool); !got {
		t.Fatalf("assistant message missing shellOutput extra: %#v", assistantMsg.Extra)
	}
	if got := toInt64ForTest(assistantMsg.Extra[msgextra.KeyFinishedAt]); got <= 0 {
		t.Fatalf("assistant finishedAt not persisted: %#v", assistantMsg.Extra)
	}
	if got, _ := assistantMsg.Extra[msgextra.KeyMsgID].(string); got == "" {
		t.Fatalf("assistant msgID not persisted: %#v", assistantMsg.Extra)
	}
}

func toInt64ForTest(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func toEnvMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if key, value, ok := strings.Cut(kv, "="); ok {
			out[key] = value
		}
	}
	return out
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	sb := fileserver.GetFileManager()
	res, err := sb.FileCreateTemp(&fsmodel.FileCreateTempRequest{
		Pattern: "quartet-test-ctrl-*",
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sb.FileDelete(&fsmodel.FileDeleteRequest{Path: res.File})
	})
	return res.File
}
