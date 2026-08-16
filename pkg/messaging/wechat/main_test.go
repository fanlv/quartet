package wechat

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "quartet-wechat-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test LOCAL_MEMORY:", err)
		os.Exit(1)
	}

	if err := os.Setenv("LOCAL_MEMORY", tmp); err != nil {
		fmt.Fprintln(os.Stderr, "set test LOCAL_MEMORY:", err)
		_ = os.RemoveAll(tmp)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
