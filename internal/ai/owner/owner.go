package owner

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type openAccessContextKey struct{}

// WithOpenAccess marks a trusted desktop, explicitly open deployment, or internal shared-state caller.
func WithOpenAccess(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAccessContextKey{}, true)
}

func OpenAccess(ctx context.Context) bool {
	open, _ := ctx.Value(openAccessContextKey{}).(bool)
	return open
}

// ID resolves the request owner without allowing an account-scoped caller to fall back to shared state.
func ID(ctx context.Context, shared bool) (string, error) {
	if userID, ok := ipc.UserIDFromContext(ctx); ok {
		return userID, nil
	}
	if shared || OpenAccess(ctx) {
		return "", nil
	}
	return "", ipc.NewError(ipc.CodeForbidden, "AI 请求缺少账号身份")
}

type executionContext struct {
	context.Context
}

func (executionContext) Value(any) any { return nil }

// AccountContext drops privilege-bearing values, preserves account metadata, and restores Open only for ownerless shared execution.
func AccountContext(ctx context.Context, ownerID string) context.Context {
	var result context.Context = executionContext{ctx}
	result = ipc.WithUserID(result, ownerID)
	if role, ok := ipc.RoleFromContext(ctx); ok {
		result = ipc.WithRole(result, role)
	}
	if sessionID := ipc.AccountSessionID(ctx); sessionID != "" {
		result = ipc.WithAccountSessionID(result, sessionID)
	}
	result = ipc.WithAccountSessionCheck(result, func(context.Context) error {
		return ipc.CheckAccountSession(context.WithoutCancel(ctx))
	})
	if eventOwner := ipc.EventOwnerID(ctx); eventOwner != "" {
		result = ipc.WithEventOwner(result, eventOwner)
	}
	if ownerID == "" && OpenAccess(ctx) {
		result = WithOpenAccess(result)
	}
	return result
}
