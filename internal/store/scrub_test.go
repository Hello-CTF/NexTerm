package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func requireFileTokenAbsent(t *testing.T, path, token string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(token)) {
		t.Fatalf("%s still contains remnant token %q", path, token)
	}
}

func requireFileTokenPresent(t *testing.T, path, token string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(token)) {
		t.Fatalf("%s does not contain expected token %q", path, token)
	}
}

func TestScrubFreeSpaceRemovesRemnantBytes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.SettingSet(ctx, "scrub.key", "remnant-token-abc"); err != nil {
		t.Fatal(err)
	}
	requireFileTokenPresent(t, path+"-wal", "remnant-token-abc")
	if err := database.SettingSet(ctx, "scrub.key", "replacement"); err != nil {
		t.Fatal(err)
	}
	if err := database.ScrubFreeSpace(ctx); err != nil {
		t.Fatal(err)
	}
	if got, found, err := database.SettingGet(ctx, "scrub.key"); err != nil || !found || got != "replacement" {
		t.Fatalf("scrub damaged live data: %q found=%v err=%v", got, found, err)
	}
	requireFileTokenAbsent(t, path, "remnant-token-abc")
	requireFileTokenAbsent(t, path+"-wal", "remnant-token-abc")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	requireFileTokenAbsent(t, path, "remnant-token-abc")
}

func TestScrubFreeSpaceOnMemoryStore(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if err := database.SettingSet(ctx, "scrub.key", "value"); err != nil {
		t.Fatal(err)
	}
	if err := database.ScrubFreeSpace(ctx); err != nil {
		t.Fatal(err)
	}
	if got, found, err := database.SettingGet(ctx, "scrub.key"); err != nil || !found || got != "value" {
		t.Fatalf("scrub damaged live data: %q found=%v err=%v", got, found, err)
	}
}

func TestScrubFreeSpaceBusyWithHeldReader(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.SettingSet(ctx, "scrub.key", "busy-token-abc"); err != nil {
		t.Fatal(err)
	}
	tx, err := database.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", "scrub.key").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if err := database.SettingSet(ctx, "scrub.key", "replacement"); !IsBusy(err) {
		t.Fatalf("write with held reader must report busy, got %v", err)
	}
	if err := database.ScrubFreeSpace(ctx); !errors.Is(err, ErrScrubBusy) {
		t.Fatalf("expected ErrScrubBusy, got %v", err)
	}
	requireFileTokenPresent(t, path+"-wal", "busy-token-abc")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := database.SettingSet(ctx, "scrub.key", "replacement"); err != nil {
		t.Fatal(err)
	}
	if err := database.ScrubFreeSpace(ctx); err != nil {
		t.Fatal(err)
	}
	if got, found, err := database.SettingGet(ctx, "scrub.key"); err != nil || !found || got != "replacement" {
		t.Fatalf("scrub damaged live data: %q found=%v err=%v", got, found, err)
	}
	requireFileTokenAbsent(t, path, "busy-token-abc")
	requireFileTokenAbsent(t, path+"-wal", "busy-token-abc")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSettingSetManyDeleteAtomicApply(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if err := database.SettingSet(ctx, "legacy.key", "legacy-value"); err != nil {
		t.Fatal(err)
	}
	if err := database.SettingSetManyDelete(ctx, map[string]string{"current.key": "current-value", "pending.key": "1"}, "legacy.key", "missing.key"); err != nil {
		t.Fatal(err)
	}
	if got, found, err := database.SettingGet(ctx, "current.key"); err != nil || !found || got != "current-value" {
		t.Fatalf("current.key = %q found=%v err=%v", got, found, err)
	}
	if got, found, err := database.SettingGet(ctx, "pending.key"); err != nil || !found || got != "1" {
		t.Fatalf("pending.key = %q found=%v err=%v", got, found, err)
	}
	if _, found, err := database.SettingGet(ctx, "legacy.key"); err != nil || found {
		t.Fatalf("legacy.key survived: found=%v err=%v", found, err)
	}
	if err := database.SettingSetManyDelete(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSettingTxCommitAndRollback(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if err := database.SettingSet(ctx, "tx.key", "before"); err != nil {
		t.Fatal(err)
	}
	if err := database.SettingTx(ctx, func(tx SettingTx) error {
		if err := tx.SettingDelete(ctx, "tx.key"); err != nil {
			t.Fatal(err)
		}
		if _, found, err := tx.SettingGet(ctx, "tx.key"); err != nil || found {
			t.Fatalf("tx must see its own delete: found=%v err=%v", found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.SettingGet(ctx, "tx.key"); err != nil || found {
		t.Fatalf("commit did not apply: found=%v err=%v", found, err)
	}
	injected := errors.New("injected tx failure")
	if err := database.SettingTx(ctx, func(tx SettingTx) error {
		if err := tx.SettingDelete(ctx, "tx.key"); err != nil {
			t.Fatal(err)
		}
		return injected
	}); !errors.Is(err, injected) {
		t.Fatalf("expected injected error, got %v", err)
	}
}

func requireAIPending(t *testing.T, database *Store, want bool) {
	t.Helper()
	raw, found, err := database.SettingGet(context.Background(), AIScrubPendingSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if want && (!found || raw == "") {
		t.Fatal("AI scrub pending marker must be set")
	}
	if !want && found && raw != "" {
		t.Fatal("AI scrub pending marker must be absent")
	}
}

func requireAIGeneration(t *testing.T, database *Store, want int64) {
	t.Helper()
	got, err := database.AISettingsGeneration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("generation = %d, want %d", got, want)
	}
}

func TestAISettingWritePathsMarkPendingAndGeneration(t *testing.T) {
	ctx := context.Background()
	plaintext := `{"version":1,"profiles":[{"id":"p1","apiKey":"direct-token","model":"m"}],"activeId":"p1"}`
	envelope := `{"version":1,"profiles":[{"id":"p1","apiKey":"` + SecretEnvelopePrefix + `abc","model":"m"}],"activeId":"p1"}`
	keyedLegacy := `{"baseUrl":"https://a.test/v1","apiKey":"legacy-token","model":"m"}`
	for _, tc := range []struct {
		name       string
		write      func(t *testing.T, database *Store)
		pending    bool
		generation int64
	}{
		{"SettingSet plaintext", func(t *testing.T, database *Store) {
			if err := database.SettingSet(ctx, AIProfilesSettingKey, plaintext); err != nil {
				t.Fatal(err)
			}
		}, true, 1},
		{"SettingSet envelope over plaintext", func(t *testing.T, database *Store) {
			if err := database.SettingSet(ctx, AIProfilesSettingKey, plaintext); err != nil {
				t.Fatal(err)
			}
			if err := database.SettingSet(ctx, AIProfilesSettingKey, envelope); err != nil {
				t.Fatal(err)
			}
		}, true, 2},
		{"SettingSet steady envelope", func(t *testing.T, database *Store) {
			if err := database.SettingSet(ctx, AIProfilesSettingKey, envelope); err != nil {
				t.Fatal(err)
			}
		}, false, 1},
		{"SettingSet corrupted", func(t *testing.T, database *Store) {
			if err := database.SettingSet(ctx, AIProfilesSettingKey, `{"profiles":[{"apiKey":"broken"`); err != nil {
				t.Fatal(err)
			}
		}, true, 1},
		{"SettingSetMany keyed legacy", func(t *testing.T, database *Store) {
			if err := database.SettingSetMany(ctx, map[string]string{AILegacySettingKey: keyedLegacy}); err != nil {
				t.Fatal(err)
			}
		}, true, 1},
		{"SettingSetManyDelete keyed legacy", func(t *testing.T, database *Store) {
			if err := database.SettingSetMany(ctx, map[string]string{AILegacySettingKey: keyedLegacy}); err != nil {
				t.Fatal(err)
			}
			if err := database.SettingSetManyDelete(ctx, map[string]string{AIProfilesSettingKey: envelope}, AILegacySettingKey); err != nil {
				t.Fatal(err)
			}
		}, true, 2},
		{"SettingDelete keyed legacy", func(t *testing.T, database *Store) {
			if err := database.SettingSet(ctx, AILegacySettingKey, keyedLegacy); err != nil {
				t.Fatal(err)
			}
			if err := database.SettingDelete(ctx, AILegacySettingKey); err != nil {
				t.Fatal(err)
			}
		}, true, 2},
		{"SettingDelete plaintext profiles", func(t *testing.T, database *Store) {
			if err := database.SettingSet(ctx, AIProfilesSettingKey, plaintext); err != nil {
				t.Fatal(err)
			}
			if err := database.SettingDelete(ctx, AIProfilesSettingKey); err != nil {
				t.Fatal(err)
			}
		}, true, 2},
		{"SettingSet unrelated key", func(t *testing.T, database *Store) {
			if err := database.SettingSet(ctx, "vault.mode", "master"); err != nil {
				t.Fatal(err)
			}
		}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := testStore(t)
			database.SetSecretProtector(stubProtector{})
			tc.write(t, database)
			requireAIPending(t, database, tc.pending)
			requireAIGeneration(t, database, tc.generation)
		})
	}
}

func TestAISettingSteadyWritesDoNotVacuum(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	database.SetSecretProtector(stubProtector{})
	envelope := `{"version":1,"profiles":[{"id":"p1","apiKey":"` + SecretEnvelopePrefix + `abc","model":"m"}],"activeId":"p1"}`
	if err := database.SettingSet(ctx, AIProfilesSettingKey, envelope); err != nil {
		t.Fatal(err)
	}
	requireAIPending(t, database, false)
}
