//go:build !unix

package shellenv

import (
	"context"
	"errors"
)

func runShell(context.Context, string, []string, []string) ([]byte, error) {
	return nil, errors.New("shell environment resolution is not supported on this platform")
}
