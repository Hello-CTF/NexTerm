package app

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestTranscriptRetentionSettingsRoundTripAndPolicy(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	policy, err := TranscriptRetentionPolicy(ctx, database)
	if err != nil || policy.MaxAge != TranscriptDefaultRetentionMaxAge || policy.MaxCount != TranscriptDefaultRetentionMaxCount {
		t.Fatalf("unset setting = %+v, %v; want the defaults", policy, err)
	}
	// A setting written before the enabled flag existed keeps cleanup on.
	if err := database.SettingSet(ctx, TranscriptRetentionSettingKey, `{"maxAgeMs":3600000,"maxCount":5}`); err != nil {
		t.Fatal(err)
	}
	policy, err = TranscriptRetentionPolicy(ctx, database)
	if err != nil || policy.MaxAge != time.Hour || policy.MaxCount != 5 {
		t.Fatalf("legacy setting = %+v, %v", policy, err)
	}

	enabled := true
	week := 7 * 24 * time.Hour
	if err := WriteTranscriptRetentionSettings(ctx, database, TranscriptRetentionSettings{
		Enabled: &enabled, MaxAgeMS: week.Milliseconds(), MaxCount: 9, UnlimitedCount: true,
	}); err != nil {
		t.Fatal(err)
	}
	policy, err = TranscriptRetentionPolicy(ctx, database)
	if err != nil || policy.MaxAge != week || policy.MaxCount != 0 {
		t.Fatalf("unlimited count = %+v, %v; want only the age limit", policy, err)
	}
	read, err := ReadTranscriptRetentionSettings(ctx, database)
	if err != nil || read.Enabled == nil || !*read.Enabled || read.MaxCount != 9 || !read.UnlimitedCount {
		t.Fatalf("read back %+v, %v", read, err)
	}

	disabled := false
	if err := WriteTranscriptRetentionSettings(ctx, database, TranscriptRetentionSettings{Enabled: &disabled, MaxAgeMS: week.Milliseconds(), MaxCount: 9}); err != nil {
		t.Fatal(err)
	}
	policy, err = TranscriptRetentionPolicy(ctx, database)
	if err != nil || policy != (store.TranscriptRetentionPolicy{}) {
		t.Fatalf("disabled cleanup = %+v, %v; want no limits", policy, err)
	}
	if err := WriteTranscriptRetentionSettings(ctx, database, TranscriptRetentionSettings{MaxAgeMS: -1}); err == nil {
		t.Fatal("a negative age must be rejected")
	}
}
