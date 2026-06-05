package logger

import (
	"log/slog"
	"strings"
	"sync/atomic"
)

// Level guard. Stored as an int32 so calls can read it without a mutex.
//
// Default is Info. Debug output is expensive (large ACP prompts, full HTTP
// bodies, etc.) and was previously always-on, which flooded stdout during
// normal operation. Anyone who wants the old verbose behavior can flip the
// level to Debug via the settings UI or SetLevel at boot.
var currentLevel atomic.Int32

const (
	levelAny   = -100
	levelDebug = int32(slog.LevelDebug)
	levelInfo  = int32(slog.LevelInfo)
	levelWarn  = int32(slog.LevelWarn)
	levelError = int32(slog.LevelError)
)

func init() {
	currentLevel.Store(levelInfo)
}

// SetLevel changes the global log level at runtime. Accepts the strings
// "debug", "info", "warn", "error" (case-insensitive). Unknown values leave
// the level unchanged and return false.
func SetLevel(name string) bool {
	lvl := parseLevel(name)
	if lvl == levelAny {
		return false
	}
	currentLevel.Store(lvl)
	return true
}

// GetLevel returns the current level as a lowercase string.
func GetLevel() string {
	switch currentLevel.Load() {
	case levelDebug:
		return "debug"
	case levelWarn:
		return "warn"
	case levelError:
		return "error"
	default:
		return "info"
	}
}

func enabled(level int32) bool {
	return level >= currentLevel.Load()
}

// DebugEnabled reports whether Debug-level output is currently on.
// Use this to gate expensive argument construction (e.g. JSON serialization)
// BEFORE calling Debugf — Go evaluates arguments before the callee, so the
// level guard inside log() can't short-circuit that work.
func DebugEnabled() bool {
	return enabled(levelDebug)
}

func parseLevel(name string) int32 {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return levelDebug
	case "info":
		return levelInfo
	case "warn", "warning":
		return levelWarn
	case "error", "err":
		return levelError
	default:
		return levelAny
	}
}

func levelRank(name string) int32 {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "DEBUG":
		return levelDebug
	case "INFO":
		return levelInfo
	case "WARN":
		return levelWarn
	case "ERROR":
		return levelError
	default:
		return levelInfo
	}
}

func normalizeLevel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return "DEBUG"
	case "warn", "warning":
		return "WARN"
	case "error", "err":
		return "ERROR"
	default:
		return "INFO"
	}
}
