package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/migrations"
)

func TestMigrationOwnershipAssignment(t *testing.T) {
	for index, test := range []struct {
		name      string
		withAdmin bool
		wantOwner string
		wantAIKey string
	}{
		{name: "no users keep shared data", withAdmin: false, wantOwner: "", wantAIKey: "ai.models"},
		{name: "first superadmin receives legacy data", withAdmin: true, wantOwner: "u-1", wantAIKey: "user.ai.models.u-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := fmt.Sprintf("file: migration-ownership-%d?mode=memory&cache=shared", index)
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			contents, err := migrations.Files.ReadFile("0001_init.sql")
			if err != nil {
				t.Fatal(err)
			}
			exec(string(contents))
			exec("INSERT INTO asset(id, kind, name, created_at, updated_at) VALUES('asset-1', 'ssh', 'asset', 1, 1)")
			exec("INSERT INTO setting(key, value, updated_at) VALUES('ai.models', '{}', 1)")
			exec(`INSERT INTO outcome_record(idempotence_key, authorization_id, kind, canonical_arguments, state, outcome, audit_state, revision, created_at)
VALUES('shared-key', 'auth-1', 'command', '{}', 'pending', 'not_attempted', 'pending', 1, 1)`)
			if test.withAdmin {
				exec("INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at) VALUES('u-1', 'root', 'superadmin', 'x', 'active', 1, 1)")
			}
			contents, err = migrations.Files.ReadFile("0023_user_ownership.sql")
			if err != nil {
				t.Fatal(err)
			}
			exec(string(contents))

			var ownerID string
			if err := db.QueryRowContext(ctx, "SELECT owner_id FROM asset WHERE id = 'asset-1'").Scan(&ownerID); err != nil {
				t.Fatal(err)
			}
			if ownerID != test.wantOwner {
				t.Fatalf("asset owner = %q, want %q", ownerID, test.wantOwner)
			}
			var aiKey string
			if err := db.QueryRowContext(ctx, "SELECT key FROM setting").Scan(&aiKey); err != nil {
				t.Fatal(err)
			}
			if aiKey != test.wantAIKey {
				t.Fatalf("AI setting key = %q, want %q", aiKey, test.wantAIKey)
			}
			var authorizationID string
			if err := db.QueryRowContext(ctx, "SELECT owner_id, authorization_id FROM outcome_record WHERE idempotence_key = 'shared-key'").Scan(&ownerID, &authorizationID); err != nil {
				t.Fatal(err)
			}
			if ownerID != test.wantOwner || authorizationID != "auth-1" {
				t.Fatalf("outcome owner = %q, authorization = %q", ownerID, authorizationID)
			}
		})
	}
}
