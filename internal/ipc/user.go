package ipc

import "context"

// userIDContextKey 由服务端在会话校验通过后注入操作用户 ID(账号用户作用域)。
// 放在 ipc 包是因为 store/profiles/sync/server 都已依赖 ipc, 身份上下文在此无循环可读。
type userIDContextKey struct{}

func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDContextKey{}, userID)
}

// UserIDFromContext 读取会话注入的用户 ID; 空串按无身份处理(调用方不得退化为全局设置)。
func UserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userIDContextKey{}).(string)
	if !ok || userID == "" {
		return "", false
	}
	return userID, true
}

// roleContextKey 由服务端在会话校验通过后注入操作者角色(账号用户作用域),
// 与 userIDContextKey 同一注入点; 消费方按 account.Role 的字面值比较。
type roleContextKey struct{}

func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleContextKey{}, role)
}

// RoleFromContext 读取会话注入的角色; 空串按无角色处理。
func RoleFromContext(ctx context.Context) (string, bool) {
	role, ok := ctx.Value(roleContextKey{}).(string)
	if !ok || role == "" {
		return "", false
	}
	return role, true
}
