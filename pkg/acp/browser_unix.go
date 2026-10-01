//go:build !windows

package acp

import "github.com/fanlv/quartet/pkg/executil"

func nonInteractiveBrowser() string {
	if path, err := executil.LookPath("true"); err == nil {
		return path
	}
	return "/usr/bin/true"
}
