//go:build windows

package acp

func nonInteractiveBrowser() string { return "cmd /c exit 0" }
