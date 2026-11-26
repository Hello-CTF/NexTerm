package guard

import "context"

type unattendedKey struct{}

// unattendedSource records what started an unattended run. A run a terminal
// event starts is timed by terminal output, so it is kept apart from
// scheduled runs.
type unattendedSource int

const (
	unattendedScheduled unattendedSource = iota + 1
	unattendedEvent
)

// WithUnattended marks a run nobody watches. It keeps an existing event
// marker, so re-marking never widens an event-triggered run.
func WithUnattended(ctx context.Context) context.Context {
	if EventTriggeredFrom(ctx) {
		return ctx
	}
	return context.WithValue(ctx, unattendedKey{}, unattendedScheduled)
}

// WithEventTriggered marks an unattended run that terminal output started.
// Such a run never uses grant rules or device grants, including rules that
// allow scheduled unattended runs.
func WithEventTriggered(ctx context.Context) context.Context {
	return context.WithValue(ctx, unattendedKey{}, unattendedEvent)
}

func UnattendedFrom(ctx context.Context) bool {
	source, _ := ctx.Value(unattendedKey{}).(unattendedSource)
	return source != 0
}

// EventTriggeredFrom reports whether a terminal event started the run.
func EventTriggeredFrom(ctx context.Context) bool {
	source, _ := ctx.Value(unattendedKey{}).(unattendedSource)
	return source == unattendedEvent
}

// InheritUnattended copies the unattended marker of from onto ctx.
func InheritUnattended(ctx, from context.Context) context.Context {
	if source, _ := from.Value(unattendedKey{}).(unattendedSource); source != 0 {
		return context.WithValue(ctx, unattendedKey{}, source)
	}
	return ctx
}

func unattendedReason(ruling Ruling) string {
	const prefix = "无人值守执行无法人工确认，已拒绝"
	if ruling.Reason == "" {
		return prefix
	}
	return prefix + ": " + ruling.Reason
}
