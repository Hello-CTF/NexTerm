package store_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestUserSessionRetentionDeletesOnlyEligibleRowsDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	now := time.Now()
	ms := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }
	day := 24 * time.Hour
	maxAge := 14 * day

	if err := db.GORM().WithContext(ctx).Create(&store.AppUser{
		ID: "user-1", Username: "retention", Role: "user", PasswordHash: "x", State: "active",
		CreatedAt: ms(-60 * day), UpdatedAt: ms(-60 * day),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GORM().WithContext(ctx).Create(&store.UserDevice{
		ID: "device-1", UserID: "user-1", Name: "laptop", Kind: "desktop", CreatedAt: ms(-60 * day),
	}).Error; err != nil {
		t.Fatal(err)
	}
	device := "device-1"
	revokedOld, revokedRecent := ms(-31*day), ms(-29*day)
	sessions := []store.UserSession{
		{ID: "live", ExpiresAt: ms(time.Hour), CreatedAt: ms(-day)},
		{ID: "expired-recent", ExpiresAt: ms(-6 * day), CreatedAt: ms(-7 * day)},
		{ID: "expired-old", ExpiresAt: ms(-8 * day), CreatedAt: ms(-9 * day), DeviceID: &device},
		{ID: "revoked-recent", ExpiresAt: ms(time.Hour), CreatedAt: ms(-2 * day), RevokedAt: &revokedRecent},
		{ID: "revoked-old", ExpiresAt: ms(time.Hour), CreatedAt: ms(-2 * day), RevokedAt: &revokedOld},
		// expires_at in the future but created before the absolute lifetime plus margin.
		{ID: "created-old", ExpiresAt: ms(time.Hour), CreatedAt: ms(-15 * day)},
	}
	for i := range sessions {
		sessions[i].UserID = "user-1"
		sessions[i].TokenHash = "hash-" + sessions[i].ID
		sessions[i].TouchedAt = sessions[i].CreatedAt
	}
	// Enough eligible rows to span several deletion batches.
	for i := 0; i < 2*128+3; i++ {
		id := "bulk-" + time.Duration(i).String()
		sessions = append(sessions, store.UserSession{
			ID: id, UserID: "user-1", TokenHash: "hash-" + id, CreatedAt: ms(-20 * day), TouchedAt: ms(-20 * day), ExpiresAt: ms(-19 * day),
		})
	}
	if err := db.GORM().WithContext(ctx).CreateInBatches(sessions, 64).Error; err != nil {
		t.Fatal(err)
	}

	result, err := db.EnforceRetention(ctx, store.RetentionPolicy{UserSessionMaxAge: maxAge})
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(3 + 2*128 + 3); result.UserSessionsDeleted != want {
		t.Fatalf("deleted %d sessions, want %d", result.UserSessionsDeleted, want)
	}
	var remaining []string
	if err := db.GORM().WithContext(ctx).Model(&store.UserSession{}).Order("id").Pluck("id", &remaining).Error; err != nil {
		t.Fatal(err)
	}
	sort.Strings(remaining)
	want := []string{"expired-recent", "live", "revoked-recent"}
	if len(remaining) != len(want) {
		t.Fatalf("remaining sessions = %v, want %v", remaining, want)
	}
	for i := range want {
		if remaining[i] != want[i] {
			t.Fatalf("remaining sessions = %v, want %v", remaining, want)
		}
	}
	var devices int64
	if err := db.GORM().WithContext(ctx).Model(&store.UserDevice{}).Where("id = ?", "device-1").Count(&devices).Error; err != nil {
		t.Fatal(err)
	}
	if devices != 1 {
		t.Fatal("deleting a session must keep its device")
	}

	repeated, err := db.EnforceRetention(ctx, store.RetentionPolicy{UserSessionMaxAge: maxAge})
	if err != nil || repeated.UserSessionsDeleted != 0 {
		t.Fatalf("repeated retention = %+v, %v", repeated, err)
	}
}
