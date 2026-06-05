package acp

import (
	"strings"
	"testing"
)

func writeToSyncBuffer(t *testing.T, b *syncBuffer, s string) {
	t.Helper()
	written, err := b.Write([]byte(s))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if written != len(s) {
		t.Fatalf("Write returned written=%d, want %d", written, len(s))
	}
}

func TestSyncBufferSmallWriteOnlyUsesHead(t *testing.T) {
	var b syncBuffer
	writeToSyncBuffer(t, &b, "hello")

	if got := b.String(); got != "hello" {
		t.Fatalf("String() = %q, want %q", got, "hello")
	}
}

func TestSyncBufferExactlyHeadSize(t *testing.T) {
	var b syncBuffer
	data := strings.Repeat("h", stderrHeadSize)
	writeToSyncBuffer(t, &b, data)

	got := b.String()
	if got != data {
		t.Fatalf("String() len=%d, want exact head len=%d", len(got), len(data))
	}
	if strings.Contains(got, "stderr omitted") {
		t.Fatalf("String() unexpectedly contains omission marker")
	}
}

func TestSyncBufferWithinCapacityDoesNotOmit(t *testing.T) {
	var b syncBuffer
	head := strings.Repeat("h", stderrHeadSize)
	tail := "tail-diagnostics"
	writeToSyncBuffer(t, &b, head)
	writeToSyncBuffer(t, &b, tail)

	got := b.String()
	if got != head+tail {
		t.Fatalf("String() len=%d, want %d without omission", len(got), len(head)+len(tail))
	}
	if strings.Contains(got, "stderr omitted") {
		t.Fatalf("String() unexpectedly contains omission marker")
	}
}

func TestSyncBufferOmitsMiddleAndKeepsTail(t *testing.T) {
	var b syncBuffer
	head := strings.Repeat("h", stderrHeadSize)
	middle := strings.Repeat("m", 1234)
	tail := strings.Repeat("t", stderrTailSize)
	writeToSyncBuffer(t, &b, head)
	writeToSyncBuffer(t, &b, middle)
	writeToSyncBuffer(t, &b, tail)

	got := b.String()
	wantMarker := "[stderr omitted 1234 bytes; preserving first 2097152 bytes and last 8388608 bytes]"
	if !strings.Contains(got, wantMarker) {
		t.Fatalf("String() missing marker %q", wantMarker)
	}
	if !strings.HasPrefix(got, strings.Repeat("h", 1024)) {
		t.Fatalf("String() does not preserve head prefix")
	}
	if !strings.HasSuffix(got, strings.Repeat("t", 1024)) {
		t.Fatalf("String() does not preserve latest tail suffix")
	}
}

func TestSyncBufferSingleWriteLargerThanCapacity(t *testing.T) {
	var b syncBuffer
	head := strings.Repeat("h", stderrHeadSize)
	middle := strings.Repeat("m", 987)
	tail := strings.Repeat("t", stderrTailSize)
	writeToSyncBuffer(t, &b, head+middle+tail)

	got := b.String()
	wantMarker := "[stderr omitted 987 bytes; preserving first 2097152 bytes and last 8388608 bytes]"
	if !strings.Contains(got, wantMarker) {
		t.Fatalf("String() missing marker %q", wantMarker)
	}
	if !strings.HasPrefix(got, strings.Repeat("h", 1024)) {
		t.Fatalf("String() does not preserve head prefix")
	}
	if !strings.HasSuffix(got, strings.Repeat("t", 1024)) {
		t.Fatalf("String() does not preserve latest tail suffix")
	}
}

func TestSyncBufferTailOverflowAcrossWrites(t *testing.T) {
	var b syncBuffer
	writeToSyncBuffer(t, &b, strings.Repeat("h", stderrHeadSize))
	writeToSyncBuffer(t, &b, strings.Repeat("x", stderrTailSize-3))
	writeToSyncBuffer(t, &b, "abcdef")

	got := b.String()
	wantMarker := "[stderr omitted 3 bytes; preserving first 2097152 bytes and last 8388608 bytes]"
	if !strings.Contains(got, wantMarker) {
		t.Fatalf("String() missing marker %q", wantMarker)
	}
	if !strings.HasSuffix(got, "abcdef") {
		t.Fatalf("String() suffix = %q, want abcdef", got[len(got)-6:])
	}
}
