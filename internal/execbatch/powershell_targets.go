package execbatch

import "context"

type powerShellTargetsKey struct{}

// WithPowerShellTargets marks a request whose command is PowerShell-safe on its own (it runs through
// "powershell -EncodedCommand"), so the resolver may accept Windows hosts that ordinary batches reject because
// their commands are written for a POSIX shell. Only in-process callers can set it; IPC input cannot.
func WithPowerShellTargets(ctx context.Context) context.Context {
	return context.WithValue(ctx, powerShellTargetsKey{}, true)
}

// PowerShellTargetsAllowed reports whether WithPowerShellTargets marked ctx.
func PowerShellTargetsAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(powerShellTargetsKey{}).(bool)
	return allowed
}
