package acp

import (
	"errors"
	"testing"
)

func resetTrackedConnsForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		trackedConnsMu.Lock()
		trackedConns = nil
		poolClosing = false
		trackedConnsMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestTrackConnRejectsRegistrationAfterCloseAllConns(t *testing.T) {
	resetTrackedConnsForTest(t)

	if err := trackConn(&Conn{}); err != nil {
		t.Fatalf("trackConn before shutdown failed: %v", err)
	}

	CloseAllConns()

	if err := trackConn(&Conn{}); !errors.Is(err, errConnPoolClosing) {
		t.Fatalf("trackConn after shutdown error = %v, want %v", err, errConnPoolClosing)
	}

	trackedConnsMu.Lock()
	defer trackedConnsMu.Unlock()
	if !poolClosing {
		t.Fatalf("poolClosing = false, want true")
	}
	if len(trackedConns) != 0 {
		t.Fatalf("trackedConns len = %d, want 0", len(trackedConns))
	}
}
