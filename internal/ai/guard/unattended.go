package guard

import "context"

type unattendedKey struct{}

// WithUnattended marks ctx as driving an unattended execution (for example a
// cron-triggered AI run). Permission providers consult UnattendedFrom to hand
// out Config{Mode: Unattended} for these contexts, so NeedsConfirm and
// Forbidden rulings are denied explicitly instead of being executed silently
// or parked on a confirmation no one will answer.
func WithUnattended(ctx context.Context) context.Context {
	return context.WithValue(ctx, unattendedKey{}, true)
}

// UnattendedFrom reports whether ctx was marked by WithUnattended.
func UnattendedFrom(ctx context.Context) bool {
	unattended, _ := ctx.Value(unattendedKey{}).(bool)
	return unattended
}

func unattendedReason(ruling Ruling) string {
	const prefix = "无人值守执行无法人工确认，已拒绝"
	if ruling.Reason == "" {
		return prefix
	}
	return prefix + ": " + ruling.Reason
}
