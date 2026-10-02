//go:build !windows

package mount

import "context"

func runInteractive(ctx context.Context, input command) (commandResult, error) {
	return runNative(ctx, input)
}
