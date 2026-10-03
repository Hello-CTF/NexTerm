package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

var testScope = Scope{Tenant: "tenant-a", Subject: "subject-a"}

func openMemoryStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newMemoryStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := t.TempDir() + "/memory.db"
	return openMemoryStore(t, path), path
}

func mustCreate(t *testing.T, store *Store, scope Scope, topic, content string) Entry {
	t.Helper()
	entry, err := store.Create(context.Background(), scope, CreateInput{Topic: topic, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func mustEnableInjection(t *testing.T, store *Store, scope Scope) Settings {
	t.Helper()
	settings, err := store.SetInjectionEnabled(context.Background(), scope, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func sequenceIDs(store *Store) {
	counter := 0
	store.newID = func() string {
		counter++
		return fmt.Sprintf("memory-%06d", counter)
	}
}

func textPointer(value string) *string {
	return &value
}

func TestCRUDIndexSettingsAndRestart(t *testing.T) {
	ctx := context.Background()
	store, path := newMemoryStore(t)
	sequenceIDs(store)
	settings, err := store.Settings(ctx, testScope)
	if err != nil || settings != (Settings{}) {
		t.Fatalf("default settings = %+v, err = %v", settings, err)
	}
	settings = mustEnableInjection(t, store, testScope)
	if !settings.InjectionEnabled || settings.Version != 1 {
		t.Fatalf("enabled settings = %+v", settings)
	}
	_, err = store.SetInjectionEnabled(ctx, testScope, false, 0)
	assertVersionConflict(t, err, 1)

	first := mustCreate(t, store, testScope, "z-last", "restart the service at 02:00")
	second := mustCreate(t, store, testScope, "a-first", "verify the health endpoint")
	if first.Version != 1 || second.Version != 1 || first.ID == second.ID {
		t.Fatalf("created entries = %+v, %+v", first, second)
	}
	edited, err := store.Edit(ctx, testScope, first.ID, 1, EditInput{
		Topic: textPointer("a-first"), Content: textPointer("restart through the unit manager"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Version != 2 || edited.Topic != "a-first" || edited.Content != "restart through the unit manager" {
		t.Fatalf("edited entry = %+v", edited)
	}
	index, err := store.Index(ctx, testScope)
	if err != nil {
		t.Fatal(err)
	}
	wantIndex := []TopicIndex{{Topic: "a-first", Entries: []IndexEntry{
		{ID: first.ID, Version: 2, UpdatedAt: edited.UpdatedAt},
		{ID: second.ID, Version: 1, UpdatedAt: second.UpdatedAt},
	}}}
	if !reflect.DeepEqual(index, wantIndex) {
		t.Fatalf("index = %#v, want %#v", index, wantIndex)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openMemoryStore(t, path)
	got, err := reopened.Get(ctx, testScope, first.ID)
	if err != nil || got != edited {
		t.Fatalf("reopened entry = %+v, want %+v, err = %v", got, edited, err)
	}
	settings, err = reopened.Settings(ctx, testScope)
	if err != nil || !settings.InjectionEnabled || settings.Version != 1 {
		t.Fatalf("reopened settings = %+v, err = %v", settings, err)
	}
	var schemaVersion int
	if err := reopened.db.QueryRow("PRAGMA user_version").Scan(&schemaVersion); err != nil || schemaVersion != SchemaVersion {
		t.Fatalf("schema version = %d, err = %v", schemaVersion, err)
	}
	if err := reopened.Delete(ctx, testScope, first.ID, 1); err == nil {
		t.Fatal("stale delete succeeded")
	} else {
		assertVersionConflict(t, err, 2)
	}
	if _, err := reopened.Get(ctx, testScope, first.ID); err != nil {
		t.Fatalf("conflicting delete removed entry: %v", err)
	}
	if err := reopened.Delete(ctx, testScope, first.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Get(ctx, testScope, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted entry read err = %v", err)
	}
	if err := reopened.Delete(ctx, testScope, first.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}

func TestFailedEditRollsBackEveryField(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	entry := mustCreate(t, store, testScope, "before", "original content")
	_, err := store.db.Exec(`CREATE TRIGGER fail_memory_edit BEFORE UPDATE ON memory_entry
		BEGIN SELECT RAISE(ABORT, 'injected edit failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Edit(ctx, testScope, entry.ID, 1, EditInput{
		Topic: textPointer("after"), Content: textPointer("replacement content"),
	})
	if err == nil || !strings.Contains(err.Error(), "injected edit failure") {
		t.Fatalf("injected failure err = %v", err)
	}
	got, getErr := store.Get(ctx, testScope, entry.ID)
	if getErr != nil || got != entry {
		t.Fatalf("entry after rollback = %+v, want %+v, err = %v", got, entry, getErr)
	}
}

func TestSecretRejectionAndExplicitRedaction(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	const secret = "do-not-persist-this-value"
	_, err := store.Create(ctx, testScope, CreateInput{Topic: "credential", Content: "api_key=" + secret})
	if !errors.Is(err, ErrSensitiveContent) {
		t.Fatalf("secret create err = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error contains secret: %v", err)
	}
	var count int
	if err := store.db.QueryRow("SELECT count(*) FROM memory_entry").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected secret persisted rows = %d, err = %v", count, err)
	}

	entry, err := store.Create(ctx, testScope, CreateInput{
		Topic: "credential", Content: "api_key=" + secret, Secrets: SecretRedact,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Redacted || strings.Contains(entry.Content, secret) || !strings.Contains(entry.Content, redactedValue) {
		t.Fatalf("redacted entry = %+v", entry)
	}
	var persisted string
	if err := store.db.QueryRow("SELECT content FROM memory_entry WHERE id = ?", entry.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, secret) {
		t.Fatalf("database contains secret: %q", persisted)
	}
	_, err = store.Edit(ctx, testScope, entry.ID, 1, EditInput{Content: textPointer("password=" + secret)})
	if !errors.Is(err, ErrSensitiveContent) {
		t.Fatalf("secret edit err = %v", err)
	}
	got, err := store.Get(ctx, testScope, entry.ID)
	if err != nil || got != entry {
		t.Fatalf("entry after rejected edit = %+v, want %+v, err = %v", got, entry, err)
	}
}

func TestRedactTextRecognizesOperationalSecrets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		content string
		secret  string
	}{
		{"password = 'long secret value'", "long secret value"},
		{"Authorization: Bearer abcdefghijklmnopqrst", "abcdefghijklmnopqrst"},
		{"-----BEGIN PRIVATE KEY-----\nprivate-material\n-----END PRIVATE KEY-----", "private-material"},
		{"db://user:private-password@example.test/database", "private-password"},
		{"eyJabcde.abcdefghijk.abcdefghijk", "eyJabcde.abcdefghijk.abcdefghijk"},
		{`{"password": "hunter2secret"}`, "hunter2secret"},
		{`{"api_key": "sk-hunter2secret"}`, "sk-hunter2secret"},
		{`"token":"hunter2secret"`, "hunter2secret"},
		{`DB_PASSWORD=hunter2secret`, "hunter2secret"},
		{`MYSQL_ROOT_PASSWORD: hunter2secret`, "hunter2secret"},
		{`export AWS_SECRET_ACCESS_KEY=hunter2secret`, "hunter2secret"},
	}
	for _, test := range tests {
		redacted, changed := RedactText(test.content)
		if !changed || strings.Contains(redacted, test.secret) {
			t.Errorf("RedactText(%q) = %q, changed = %v", test.content, redacted, changed)
		}
	}
	value := "backup runs at 02:00"
	redacted, changed := RedactText(value)
	if changed || redacted != value {
		t.Fatalf("safe content changed to %q", redacted)
	}
}

func TestJSONAndEnvSecretFormatsRejectedRedactedAndInjectedSafely(t *testing.T) {
	ctx := context.Background()
	formats := []struct{ name, content, secret string }{
		{"json password", `{"password": "hunter2secret"}`, "hunter2secret"},
		{"json api key", `{"api_key": "sk-hunter2secret"}`, "sk-hunter2secret"},
		{"json token", `"token":"hunter2secret"`, "hunter2secret"},
		{"env password", `DB_PASSWORD=hunter2secret`, "hunter2secret"},
		{"env prefixed password", `MYSQL_ROOT_PASSWORD: hunter2secret`, "hunter2secret"},
		{"env export access key", `export AWS_SECRET_ACCESS_KEY=hunter2secret`, "hunter2secret"},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			store, _ := newMemoryStore(t)
			_, err := store.Create(ctx, testScope, CreateInput{Topic: "credential", Content: format.content})
			if !errors.Is(err, ErrSensitiveContent) {
				t.Fatalf("default create err = %v", err)
			}
			if strings.Contains(err.Error(), format.secret) {
				t.Fatalf("error contains secret: %v", err)
			}
			var count int
			if err := store.db.QueryRow("SELECT count(*) FROM memory_entry").Scan(&count); err != nil || count != 0 {
				t.Fatalf("rejected secret persisted rows = %d, err = %v", count, err)
			}

			entry, err := store.Create(ctx, testScope, CreateInput{
				Topic: "credential", Content: format.content, Secrets: SecretRedact,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !entry.Redacted || strings.Contains(entry.Content, format.secret) || !strings.Contains(entry.Content, redactedValue) {
				t.Fatalf("redacted entry = %+v", entry)
			}
			var persisted string
			if err := store.db.QueryRow("SELECT content FROM memory_entry WHERE id = ?", entry.ID).Scan(&persisted); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(persisted, format.secret) {
				t.Fatalf("database contains secret: %q", persisted)
			}

			mustEnableInjection(t, store, testScope)
			injection, err := store.Inject(ctx, testScope, nil, Selection{}, Budget{})
			if err != nil {
				t.Fatal(err)
			}
			if injection.Memory == nil || strings.Contains(injection.Memory.Content, format.secret) {
				t.Fatalf("prompt contains secret: %+v", injection)
			}

			if _, err := store.db.Exec("UPDATE memory_entry SET content = ? WHERE id = ?", format.content, entry.ID); err != nil {
				t.Fatal(err)
			}
			injection, err = store.Inject(ctx, testScope, nil, Selection{}, Budget{})
			if err != nil {
				t.Fatal(err)
			}
			if injection.Memory == nil || strings.Contains(injection.Memory.Content, format.secret) || injection.Redactions != 1 {
				t.Fatalf("secret injection = %+v", injection)
			}
		})
	}
}

func TestOpenAcceptsRelativeDatabasePaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Chdir(dir)
	store, err := Open(ctx, filepath.Join("rel", "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	entry := mustCreate(t, store, testScope, "operations", "relative path content")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rel", "memory.db")); err != nil {
		t.Fatalf("relative database not created under working directory: %v", err)
	}
	bare, err := Open(ctx, "memory.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := bare.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "memory.db")); err != nil {
		t.Fatalf("bare database not created under working directory: %v", err)
	}
	reopened := openMemoryStore(t, filepath.Join("rel", "memory.db"))
	got, err := reopened.Get(ctx, testScope, entry.ID)
	if err != nil || got != entry {
		t.Fatalf("reopened relative entry = %+v, want %+v, err = %v", got, entry, err)
	}
}

func TestOpenAcceptsSpecialCharacterPaths(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mem #1?% dir", "memory #1?%.db")
	store := openMemoryStore(t, path)
	entry := mustCreate(t, store, testScope, "operations", "special path content")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openMemoryStore(t, path)
	got, err := reopened.Get(ctx, testScope, entry.ID)
	if err != nil || got != entry {
		t.Fatalf("reopened special-path entry = %+v, want %+v, err = %v", got, entry, err)
	}
}

func TestScopeIsolationForReadEditDeleteAndInjection(t *testing.T) {
	ctx := context.Background()
	store, _ := newMemoryStore(t)
	entry := mustCreate(t, store, testScope, "private", "owner only")
	other := Scope{Tenant: testScope.Tenant, Subject: "subject-b"}
	if _, err := store.Get(ctx, other, entry.ID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("cross-scope read err = %v", err)
	}
	if _, err := store.Edit(ctx, other, entry.ID, 1, EditInput{Content: textPointer("stolen")}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("cross-scope edit err = %v", err)
	}
	if err := store.Delete(ctx, other, entry.ID, 1); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("cross-scope delete err = %v", err)
	}
	if _, err := store.Get(ctx, Scope{}, entry.ID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("empty-scope read err = %v", err)
	}
	index, err := store.Index(ctx, other)
	if err != nil || len(index) != 0 {
		t.Fatalf("cross-scope index = %+v, err = %v", index, err)
	}
	mustEnableInjection(t, store, other)
	injection, err := store.Inject(ctx, other, []*schema.Message{schema.UserMessage("question")}, Selection{IDs: []string{entry.ID}}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if injection.Memory != nil || len(injection.Selected) != 0 || strings.Contains(injection.Messages[0].Content, "owner only") {
		t.Fatalf("cross-scope injection = %+v", injection)
	}
	got, err := store.Get(ctx, testScope, entry.ID)
	if err != nil || got != entry {
		t.Fatalf("entry after unauthorized operations = %+v, err = %v", got, err)
	}
}

func TestConcurrentEditsAcrossStoresHonorCAS(t *testing.T) {
	ctx := context.Background()
	first, path := newMemoryStore(t)
	second := openMemoryStore(t, path)
	entry := mustCreate(t, first, testScope, "shared", "version one")
	const contenders = 8
	start := make(chan struct{})
	errs := make(chan error, contenders)
	results := make(chan Entry, contenders)
	var group sync.WaitGroup
	for index := 0; index < contenders; index++ {
		store := first
		if index%2 == 1 {
			store = second
		}
		group.Add(1)
		go func(index int, store *Store) {
			defer group.Done()
			<-start
			edited, err := store.Edit(ctx, testScope, entry.ID, 1, EditInput{Content: textPointer(fmt.Sprintf("writer-%d", index))})
			if err != nil {
				errs <- err
				return
			}
			results <- edited
		}(index, store)
	}
	close(start)
	group.Wait()
	close(errs)
	close(results)
	var winner Entry
	for err := range errs {
		assertVersionConflict(t, err, 2)
	}
	for result := range results {
		if winner.ID != "" {
			t.Fatalf("multiple CAS winners: %+v and %+v", winner, result)
		}
		winner = result
	}
	if winner.ID == "" || winner.Version != 2 {
		t.Fatalf("winner = %+v", winner)
	}
	got, err := first.Get(ctx, testScope, entry.ID)
	if err != nil || got != winner {
		t.Fatalf("final entry = %+v, winner = %+v, err = %v", got, winner, err)
	}
}

func TestFutureSchemaVersionIsRejected(t *testing.T) {
	ctx := context.Background()
	store, path := newMemoryStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "unsupported semantic memory schema version") {
		t.Fatalf("future schema open err = %v", err)
	}
}

func assertVersionConflict(t *testing.T, err error, actual uint64) {
	t.Helper()
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) || conflict.Actual != actual {
		t.Fatalf("conflict = %#v, want actual %d", conflict, actual)
	}
}
