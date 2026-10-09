package ipc

import (
	"context"
	"testing"
)

func TestUserIDContextRoundtrip(t *testing.T) {
	if _, ok := UserIDFromContext(context.Background()); ok {
		t.Fatal("empty context must not carry a user ID")
	}
	ctx := WithUserID(context.Background(), "u-1")
	userID, ok := UserIDFromContext(ctx)
	if !ok || userID != "u-1" {
		t.Fatalf("UserIDFromContext = %q, %v", userID, ok)
	}
	// 空串身份视为未注入, 调用方按无身份处理
	if _, ok := UserIDFromContext(WithUserID(context.Background(), "")); ok {
		t.Fatal("empty user ID must not count as an identity")
	}
}

func TestRoleContextRoundtrip(t *testing.T) {
	if _, ok := RoleFromContext(context.Background()); ok {
		t.Fatal("empty context must not carry a role")
	}
	ctx := WithRole(context.Background(), "superadmin")
	role, ok := RoleFromContext(ctx)
	if !ok || role != "superadmin" {
		t.Fatalf("RoleFromContext = %q, %v", role, ok)
	}
	if _, ok := RoleFromContext(WithRole(context.Background(), "")); ok {
		t.Fatal("empty role must not count as a role")
	}
}
