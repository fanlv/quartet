//go:build !darwin

package usage

import (
	"context"
	"fmt"
)

func loadAntigravityOAuth(_ context.Context) (*antigravityOAuthCredentials, error) {
	creds, err := readAntigravityOAuthFile()
	if err != nil {
		return nil, fmt.Errorf("load agy OAuth credentials failed: %w (run `agy` and sign in)", err)
	}
	return creds, nil
}
