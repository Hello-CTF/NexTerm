package agent

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type ScopeValidator interface {
	ValidateScope(context.Context, tools.Scope) error
}

type ScopeResolver interface {
	ResolveScope(context.Context, string, tools.Scope) (tools.Scope, error)
}

type ScopeValidatorFunc func(context.Context, tools.Scope) error

func (f ScopeValidatorFunc) ValidateScope(ctx context.Context, scope tools.Scope) error {
	return f(ctx, scope)
}

func (r *Runner) validateScope(ctx context.Context, scope tools.Scope) error {
	if r.config.ScopeValidator == nil {
		return nil
	}
	return r.config.ScopeValidator.ValidateScope(ctx, scope)
}

func (r *Runner) resolveScope(ctx context.Context, conversationID string, scope tools.Scope) (tools.Scope, error) {
	if r.config.ScopeResolver != nil {
		return r.config.ScopeResolver.ResolveScope(ctx, conversationID, scope)
	}
	return scope, r.validateScope(ctx, scope)
}

func (r *Runner) validateExecutionRequest(ctx context.Context) error {
	if !r.config.ServerMode || ipc.OwnerID(ctx) != "" || owner.OpenAccess(ctx) {
		return nil
	}
	return ipc.NewError(ipc.CodeForbidden, "AI 请求缺少账号身份")
}

func conversationExecutionContext(ctx context.Context, ownerID string) context.Context {
	if ownerID == "" {
		return ctx
	}
	result := owner.AccountContext(ctx, ownerID)
	if guard.UnattendedFrom(ctx) {
		result = guard.WithUnattended(result)
	}
	return result
}
