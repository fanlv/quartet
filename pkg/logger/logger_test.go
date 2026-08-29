package logger

import (
	"strings"
	"testing"
	"time"
)

func TestBufferRespectsLevel(t *testing.T) {
	defer SetLevel("info") // restore default
	ClearBuffer()

	SetLevel("warn")
	Info("ignored info")
	Warn("kept warn %d", 1)
	Error("kept error")

	entries := RecentEntries(Filter{Limit: 100})
	if len(entries) != 2 {
		t.Fatalf("want 2 entries (warn+error), got %d", len(entries))
	}
	// newest first
	if entries[0].Level != "ERROR" || entries[1].Level != "WARN" {
		t.Fatalf("unexpected entry order: %#v", entries)
	}
	if !strings.Contains(entries[1].Message, "kept warn 1") {
		t.Fatalf("format args were not applied: %q", entries[1].Message)
	}
}

func TestSetLevelRejectsUnknown(t *testing.T) {
	if SetLevel("bogus") {
		t.Fatal("expected SetLevel to reject unknown level")
	}
}

func TestFilterByMinLevelAndKeyword(t *testing.T) {
	defer SetLevel("info")
	ClearBuffer()
	SetLevel("debug")

	Info("[mod] hello")
	Warn("[mod] watch out")
	Error("[other] boom")

	got := RecentEntries(Filter{MinLevel: "warn"})
	if len(got) != 2 {
		t.Fatalf("want 2 (warn+error), got %d", len(got))
	}

	got = RecentEntries(Filter{Keyword: "boom"})
	if len(got) != 1 || got[0].Level != "ERROR" {
		t.Fatalf("keyword filter broken: %#v", got)
	}

	got = RecentEntries(Filter{Keyword: "MOD"})
	if len(got) != 2 {
		t.Fatalf("keyword should be case-insensitive: %#v", got)
	}
}

func TestSinceCursor(t *testing.T) {
	defer SetLevel("info")
	ClearBuffer()
	SetLevel("debug")

	Info("a")
	first := RecentEntries(Filter{Limit: 1})[0].ID

	Info("b")
	Info("c")

	got := RecentEntries(Filter{Since: first})
	if len(got) != 2 {
		t.Fatalf("since cursor should return only newer entries, got %d", len(got))
	}
}

func TestAppendWithTimePreservesFrontendSource(t *testing.T) {
	defer SetLevel("info")
	ClearBuffer()
	SetLevel("debug")

	AppendWithTime(time.Now(), "error", "frontend/window.error", "Cannot read property 'foo' of undefined")

	entries := RecentEntries(Filter{Limit: 10})
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Source != "frontend/window.error" {
		t.Fatalf("AppendWithTime should set source: got %q", entries[0].Source)
	}
}

// TestKindFilterAppliesBeforeLimit guards against a regression where the
// settings UI would ask for "backend" logs but the server returned the most
// recent N entries of any kind, leaving the backend view incomplete whenever
// frontend chatter dominated recent activity.
func TestKindFilterAppliesBeforeLimit(t *testing.T) {
	defer SetLevel("info")
	ClearBuffer()
	SetLevel("debug")

	// Interleave 50 backend + 50 frontend entries. With limit=20 and no kind
	// filter, the newest-first walk would never reach a backend entry once a
	// run of frontend entries fills the cap.
	for i := range 50 {
		Info("backend %d", i)
		AppendWithTime(time.Now(), "info", "frontend/console", "frontend log")
	}

	// Sanity check: with no kind filter and limit=20 you can easily miss
	// backend entries depending on interleave order.
	mixed := RecentEntries(Filter{Limit: 20})
	if len(mixed) != 20 {
		t.Fatalf("mixed query should fill the limit: got %d", len(mixed))
	}

	backend := RecentEntries(Filter{Kind: KindBackend, Limit: 20})
	if len(backend) != 20 {
		t.Fatalf("kind=backend should fill the limit with backend-only entries: got %d", len(backend))
	}
	for _, e := range backend {
		if strings.HasPrefix(e.Source, "frontend") {
			t.Fatalf("kind=backend leaked frontend entry: %#v", e)
		}
	}

	frontend := RecentEntries(Filter{Kind: KindFrontend, Limit: 20})
	if len(frontend) != 20 {
		t.Fatalf("kind=frontend should fill the limit with frontend-only entries: got %d", len(frontend))
	}
	for _, e := range frontend {
		if !strings.HasPrefix(e.Source, "frontend") {
			t.Fatalf("kind=frontend leaked backend entry: %#v", e)
		}
	}

	// Unknown kind values must not silently empty the list — the UI relies on
	// "no filter" semantics so a typo never produces a permanently blank view.
	loose := RecentEntries(Filter{Kind: "bogus", Limit: 20})
	if len(loose) != 20 {
		t.Fatalf("unknown kind should disable the filter, got %d", len(loose))
	}
}
