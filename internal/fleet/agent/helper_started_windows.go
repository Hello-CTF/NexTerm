//go:build windows

package agent

import "context"

func helperStartedAt(context.Context, int) string { return "" }
