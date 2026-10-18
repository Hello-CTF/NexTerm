package ipc

import "context"

type accountSessionCheckKey struct{}
type accountSessionIDKey struct{}

func WithAccountSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, accountSessionIDKey{}, id)
}

func AccountSessionID(ctx context.Context) string {
	id, _ := ctx.Value(accountSessionIDKey{}).(string)
	return id
}

// WithAccountSessionCheck carries a server-owned check for a browser login session.
// Resource creators call it after dialing so a request that outlives logout cannot retain a new connection.
func WithAccountSessionCheck(ctx context.Context, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, accountSessionCheckKey{}, check)
}

func CheckAccountSession(ctx context.Context) error {
	check, _ := ctx.Value(accountSessionCheckKey{}).(func(context.Context) error)
	if check == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return check(ctx)
}

// userIDContextKey is where the server injects the operating user ID after session verification (account user scope).
// It lives in the ipc package because store/profiles/sync/server all already depend on ipc, so the identity context is readable here without an import cycle.
type userIDContextKey struct{}

func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDContextKey{}, userID)
}

// UserIDFromContext reads the session-injected user ID; an empty string counts as no identity (callers must not fall back to global settings).
func UserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userIDContextKey{}).(string)
	if !ok || userID == "" {
		return "", false
	}
	return userID, true
}

func OwnerID(ctx context.Context) string {
	userID, _ := UserIDFromContext(ctx)
	return userID
}

func OwnerCondition(ctx context.Context, column string) (string, []any) {
	if OwnerID(ctx) == "" {
		return "", nil
	}
	return " AND " + column + " = ?", []any{OwnerID(ctx)}
}

// roleContextKey is where the server injects the operator role after session verification (account user scope),
// at the same injection point as userIDContextKey; consumers compare against the literal values of account.Role.
type roleContextKey struct{}

func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleContextKey{}, role)
}

// RoleFromContext reads the session-injected role; an empty string counts as no role.
func RoleFromContext(ctx context.Context) (string, bool) {
	role, ok := ctx.Value(roleContextKey{}).(string)
	if !ok || role == "" {
		return "", false
	}
	return role, true
}
