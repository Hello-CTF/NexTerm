package guard

import "context"

type unattendedKey struct{}

func WithUnattended(ctx context.Context) context.Context {
	return context.WithValue(ctx, unattendedKey{}, true)
}

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
