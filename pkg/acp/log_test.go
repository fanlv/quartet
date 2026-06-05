package acp

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"testing"
)

func TestIsBenignStdioCloseLog(t *testing.T) {
	pathErrClosed := &fs.PathError{Op: "close", Path: "|1", Err: os.ErrClosed}
	wrappedClosed := fmt.Errorf("teardown: %w", os.ErrClosed)
	otherErr := errors.New("real failure")

	cases := []struct {
		name   string
		format string
		args   []any
		want   bool
	}{
		{
			name:   "stdio writer with bare ErrClosed",
			format: "close stdio writer: %v",
			args:   []any{os.ErrClosed},
			want:   true,
		},
		{
			name:   "stdio writer with PathError wrapping ErrClosed",
			format: "close stdio writer: %v",
			args:   []any{pathErrClosed},
			want:   true,
		},
		{
			name:   "stdio reader with wrapped ErrClosed",
			format: "close stdio reader: %v",
			args:   []any{wrappedClosed},
			want:   true,
		},
		{
			name:   "stdio writer with SDK [ACP-SDK] prefix",
			format: "[ACP-SDK] close stdio writer: %v",
			args:   []any{os.ErrClosed},
			want:   true,
		},
		{
			name:   "stdio writer with ErrClosedPipe",
			format: "close stdio writer: %v",
			args:   []any{io.ErrClosedPipe},
			want:   true,
		},
		{
			name:   "stdio writer with unrelated error keeps ERROR",
			format: "close stdio writer: %v",
			args:   []any{otherErr},
			want:   false,
		},
		{
			name:   "unrelated format keeps ERROR even if err is closed",
			format: "broken pipe: %v",
			args:   []any{os.ErrClosed},
			want:   false,
		},
		{
			name:   "stdio writer with no args keeps ERROR",
			format: "close stdio writer: %v",
			args:   nil,
			want:   false,
		},
		{
			name:   "stdio writer with non-error arg keeps ERROR",
			format: "close stdio writer: %v",
			args:   []any{"oops"},
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isBenignStdioCloseLog(tc.format, tc.args)
			if got != tc.want {
				t.Fatalf("isBenignStdioCloseLog(%q, %#v) = %v, want %v", tc.format, tc.args, got, tc.want)
			}
		})
	}
}
