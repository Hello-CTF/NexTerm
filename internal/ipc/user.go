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
