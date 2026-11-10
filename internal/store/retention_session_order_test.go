package store

import (
	"context"
	"testing"
	"time"
)

func TestRetentionRunsSessionCleanupAfterAnEarlierTableFails(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour).UnixMilli()
	if err := db.db.WithContext(ctx).Create(&AppUser{ID: "u", Username: "u", Role: "user", PasswordHash: "x", State: "active", CreatedAt: old, UpdatedAt: old}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.db.WithContext(ctx).Create(&UserSession{ID: "s", UserID: "u", TokenHash: "h", CreatedAt: old, TouchedAt: old, ExpiresAt: old}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.db.Exec("DROP TABLE audit_log").Error; err != nil {
		t.Fatal(err)
	}
	_, err := db.EnforceRetention(ctx, RetentionPolicy{AuditMaxAge: time.Hour, UserSessionMaxAge: 14 * 24 * time.Hour})
	if err == nil {
		t.Fatal("expected the audit retention failure to be reported")
	}
	var remaining int64
	if err := db.db.WithContext(ctx).Model(&UserSession{}).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("expired session survived after an earlier table failed: %d", remaining)
	}
}
