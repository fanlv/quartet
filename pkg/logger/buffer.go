package logger

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Entry is a single log record captured by the in-memory ring buffer. It is
// exposed through the /api/v1/logs endpoint so the settings UI can show
// recent backend logs without having to tail stdout.
type Entry struct {
	ID        uint64    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Source    string    `json:"source"`
	Message   string    `json:"message"`
}

// Filter narrows a RecentEntries query. A zero-value Filter returns everything
// the buffer still holds (newest first).
//
// Kind partitions entries into the high-level "backend" / "frontend" buckets
// the settings UI exposes. Frontend entries are tagged with a "frontend"
// source prefix by the /api/v1/logs/frontend handler; everything else is
// treated as backend. Use Sources for exact-match component filtering.
type Filter struct {
	MinLevel string
	Kind     string
	Sources  []string
	Since    uint64
	Limit    int
	Keyword  string
}

const (
	// KindBackend keeps entries whose source does not start with "frontend".
	KindBackend = "backend"
	// KindFrontend keeps entries whose source starts with "frontend".
	KindFrontend = "frontend"
)

const defaultBufferCap = 4000

// buffer is a fixed-size ring buffer of log entries. Writes are O(1). Reads
// snapshot the buffer under a read lock so long queries from the UI cannot
// block the hot-path log calls.
type buffer struct {
	mu     sync.RWMutex
	data   []Entry
	head   int // next write position
	size   int
	nextID uint64
}

var logBuffer = &buffer{data: make([]Entry, defaultBufferCap)}

// Append records an entry in the ring buffer. This is best-effort: it never
// returns an error and must not block the caller on slow consumers.
func (b *buffer) Append(level, source, message string) {
	id := atomic.AddUint64(&b.nextID, 1)
	e := Entry{
		ID:        id,
		Timestamp: time.Now(),
		Level:     level,
		Source:    source,
		Message:   message,
	}
	b.mu.Lock()
	b.data[b.head] = e
	b.head = (b.head + 1) % len(b.data)
	if b.size < len(b.data) {
		b.size++
	}
	b.mu.Unlock()
}

// Recent returns entries matching the filter, newest first. The result is a
// freshly allocated slice so the caller can mutate it safely.
func (b *buffer) Recent(f Filter) []Entry {
	minLevel := parseLevel(f.MinLevel)
	sourceSet := toSet(f.Sources)
	kind := strings.ToLower(strings.TrimSpace(f.Kind))
	limit := f.Limit
	if limit <= 0 || limit > len(b.data) {
		limit = len(b.data)
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]Entry, 0, limit)
	// Walk newest -> oldest: start at head-1 and decrement.
	for i := 0; i < b.size && len(out) < limit; i++ {
		idx := (b.head - 1 - i + len(b.data)) % len(b.data)
		e := b.data[idx]
		if f.Since != 0 && e.ID <= f.Since {
			break
		}
		if minLevel != levelAny && levelRank(e.Level) < minLevel {
			continue
		}
		if !kindMatches(kind, e.Source) {
			continue
		}
		if len(sourceSet) > 0 {
			if _, ok := sourceSet[e.Source]; !ok {
				continue
			}
		}
		if f.Keyword != "" && !containsFold(e.Message, f.Keyword) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// kindMatches reports whether an entry's source belongs to the requested
// high-level bucket. An empty kind disables the filter.
func kindMatches(kind, source string) bool {
	switch kind {
	case "":
		return true
	case KindFrontend:
		return strings.HasPrefix(source, "frontend")
	case KindBackend:
		return !strings.HasPrefix(source, "frontend")
	default:
		// Unknown kind values are treated as "no filter" so the UI never ends
		// up with a permanently empty list because of a typo in the query.
		return true
	}
}

// Clear drops every buffered entry. Used by the settings UI.
func (b *buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.data {
		b.data[i] = Entry{}
	}
	b.head = 0
	b.size = 0
}

// RecentEntries is the package-level accessor used by HTTP handlers.
func RecentEntries(f Filter) []Entry { return logBuffer.Recent(f) }

// ClearBuffer drops every buffered backend + frontend entry.
func ClearBuffer() { logBuffer.Clear() }

// AppendWithTime records an externally supplied message while preserving the
// caller's timestamp. Used when replaying frontend logs whose clock may
// lead or lag the server.
// Used when replaying frontend logs whose clock may lead/lag the server.
func AppendWithTime(ts time.Time, level, source, message string) {
	id := atomic.AddUint64(&logBuffer.nextID, 1)
	e := Entry{
		ID:        id,
		Timestamp: ts,
		Level:     normalizeLevel(level),
		Source:    source,
		Message:   message,
	}
	logBuffer.mu.Lock()
	logBuffer.data[logBuffer.head] = e
	logBuffer.head = (logBuffer.head + 1) % len(logBuffer.data)
	if logBuffer.size < len(logBuffer.data) {
		logBuffer.size++
	}
	logBuffer.mu.Unlock()
}

func containsFold(s, sub string) bool {
	if sub == "" {
		return true
	}
	ls, lsub := len(s), len(sub)
	if lsub > ls {
		return false
	}
	for i := 0; i+lsub <= ls; i++ {
		if equalFold(s[i:i+lsub], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func toSet(ss []string) map[string]struct{} {
	if len(ss) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		if s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}
