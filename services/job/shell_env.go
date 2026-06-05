package job

import (
	"os"
	"strings"
	"sync/atomic"
)

const envShellPassthrough = "QUARTET_SHELL_ENV_PASSTHROUGH"

var shellEnvReservedExact = map[string]struct{}{
	// The control file path is injected per command in prepareShellProcess.
	// Inheriting a stale parent value can make a script write STOP/SET_VAR to
	// the wrong file.
	"QUARTET_CONTROL": {},
}

var shellEnvSensitiveExact = map[string]struct{}{
	"API_KEY":                  {},
	"ACCESS_KEY":               {},
	"SECRET":                   {},
	"KEY":                      {},
	"TOKEN":                    {},
	"PASSWORD":                 {},
	"COHERE_API_KEY":           {},
	"HF_TOKEN":                 {},
	"HUGGINGFACEHUB_API_TOKEN": {},
}

// shellEnvDefaultPassthrough contains environment variable names that would
// otherwise be caught by the sensitive fragment/suffix rules (e.g. _TOKEN)
// but are safe to pass through by default in a single-user scenario. These
// are common development tool tokens that shell scripts frequently need.
//
// Quartet runs on the user's personal machine (single-user), so tokens that
// the runtime itself depends on should also be available to shell tasks —
// otherwise scripts that call the same services (model inference, crawling,
// etc.) will fail with missing-key errors.
var shellEnvDefaultPassthrough = map[string]struct{}{
	// VCS tokens
	"GITHUB_TOKEN": {},
	"GITLAB_TOKEN": {},

	// Volcano Engine / ARK (model inference used by Quartet itself)
	"ARK_API_KEY": {},
	"ARK_MODEL":   {},
	"ARK_BASE_URL": {},

	// Anthropic (used by Quartet for title generation / agent invocation)
	"ANTHROPIC_API_KEY":    {},
	"ANTHROPIC_AUTH_TOKEN": {},

	// Firecrawl (web crawling service used by Quartet skills)
	"FIRECRAWL_API_KEY": {},

	// OpenAI-compatible (often used as alternative model backend)
	"OPENAI_API_KEY":  {},
	"OPENAI_BASE_URL": {},
}

var shellEnvSensitivePrefixes = []string{
	"AWS_",
	"OPENAI_",
	"ANTHROPIC_",
	"COHERE_",
	"AZURE_OPENAI_",
	"GOOGLE_",
	"GEMINI_",
	"MISTRAL_",
	"DEEPSEEK_",
	"DASHSCOPE_",
	"ARK_",
	"VOLC_",
	"BYTEPLUS_",
}

var shellEnvSensitiveFragments = []string{
	"_TOKEN",
	"_SECRET",
	"_PASSWORD",
	"_API_KEY",
	"_ACCESS_KEY",
	"PRIVATE_KEY",
	"CREDENTIAL",
}

var shellEnvSensitiveSuffixes = []string{
	// Catch provider keys that are commonly named DEEPSEEK_KEY, GEMINI_KEY,
	// MISTRAL_KEY, etc. Use suffix matching instead of a broad "KEY" fragment
	// so unrelated variables such as KEYBOARD_LAYOUT can still pass through.
	"_KEY",
}

func parseShellEnvPassthrough(raw string) map[string]struct{} {
	allow := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		key := strings.ToUpper(strings.TrimSpace(part))
		if key == "" || key == "QUARTET_CONTROL" {
			continue
		}
		allow[key] = struct{}{}
	}
	return allow
}

type shellEnvExtraCache struct {
	raw   string
	allow map[string]struct{}
}

var cachedShellEnvExtra atomic.Pointer[shellEnvExtraCache]

func shellEnvExtra() map[string]struct{} {
	raw := os.Getenv(envShellPassthrough)
	if snap := cachedShellEnvExtra.Load(); snap != nil && snap.raw == raw {
		return snap.allow
	}
	allow := parseShellEnvPassthrough(raw)
	cachedShellEnvExtra.Store(&shellEnvExtraCache{raw: raw, allow: allow})
	return allow
}

func isAllowedShellEnvKey(key string, extra map[string]struct{}) bool {
	up := strings.ToUpper(key)
	if _, reserved := shellEnvReservedExact[up]; reserved {
		return false
	}
	// Default passthrough for common dev-tool tokens (GITHUB_TOKEN, etc.)
	// that are safe in a single-user scenario.
	if _, ok := shellEnvDefaultPassthrough[up]; ok {
		return true
	}
	// Operators can explicitly pass through a key that would otherwise be
	// filtered as sensitive. This keeps the default safe while still supporting
	// scripts that intentionally need a token/credential.
	if _, ok := extra[up]; ok {
		return true
	}
	if _, sensitive := shellEnvSensitiveExact[up]; sensitive {
		return false
	}
	for _, prefix := range shellEnvSensitivePrefixes {
		if strings.HasPrefix(up, prefix) {
			return false
		}
	}
	for _, fragment := range shellEnvSensitiveFragments {
		if strings.Contains(up, fragment) {
			return false
		}
	}
	for _, suffix := range shellEnvSensitiveSuffixes {
		if strings.HasSuffix(up, suffix) {
			return false
		}
	}
	return true
}

func buildShellEnvWithFiltered(src []string) ([]string, []string) {
	extra := shellEnvExtra()
	out := make([]string, 0, len(src))
	filtered := make([]string, 0)
	for _, kv := range src {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key := kv[:eq]
		if !isAllowedShellEnvKey(key, extra) {
			filtered = append(filtered, key)
			continue
		}
		out = append(out, kv)
	}
	return out, filtered
}

func buildShellEnv(src []string) []string {
	out, _ := buildShellEnvWithFiltered(src)
	return out
}

// sanitizedShellEnv returns the parent environment for user shell scripts with
// dangerous/reserved variables removed. Quartet shell steps are expected to
// behave like the user's local terminal, so common toolchain variables (Go,
// Rust, Node, Python, XDG, proxies, etc.) pass through by default. Sensitive
// credentials are filtered unless explicitly listed in
// QUARTET_SHELL_ENV_PASSTHROUGH.
func sanitizedShellEnv() []string {
	return buildShellEnv(os.Environ())
}

func sanitizedShellEnvWithFiltered() ([]string, []string) {
	return buildShellEnvWithFiltered(os.Environ())
}
