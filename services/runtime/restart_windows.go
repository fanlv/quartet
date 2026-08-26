//go:build windows

package runtime

import (
	"context"
	"fmt"
)

func validateWebRestartSupported() error {
	return fmt.Errorf("restart web is not supported on Windows; restart quartet-web from the host service manager")
}

func restartWeb(context.Context, string, string) error {
	return validateWebRestartSupported()
}
