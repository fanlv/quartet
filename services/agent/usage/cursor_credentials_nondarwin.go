//go:build !darwin

package usage

import "context"

// readCursorAuth loads the cursor-agent login token from auth.json. The CLI
// only uses the macOS keychain on Darwin; Linux and Windows keep the file store.
func readCursorAuth(_ context.Context) (cursorAuthFile, string, error) {
	return readCursorAuthFile()
}
