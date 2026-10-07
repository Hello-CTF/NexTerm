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
