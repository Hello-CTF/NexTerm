package store

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/migrations"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestParseBackend(t *testing.T) {
	for raw, want := range map[string]Backend{
		"":           BackendSQLite,
		"sqlite":     BackendSQLite,
		"SQLite":     BackendSQLite,
		"postgres":   BackendPostgres,
		"PostgreSQL": BackendPostgres,
	} {
		got, err := ParseBackend(raw)
		if err != nil || got != want {
			t.Fatalf("ParseBackend(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"mysql", "postgre", "sql", "postgres postgres"} {
		if _, err := ParseBackend(raw); err == nil {
			t.Fatalf("ParseBackend(%q) succeeded", raw)
		}
	}
}

func TestRebindDollarPlaceholders(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM t WHERE id = ?":                      "SELECT * FROM t WHERE id = $1",
		"INSERT INTO t(a, b) VALUES(?, ?)":                  "INSERT INTO t(a, b) VALUES($1, $2)",
		"SELECT * FROM t WHERE a = ? AND b = ? AND c = ?":   "SELECT * FROM t WHERE a = $1 AND b = $2 AND c = $3",
		"SELECT * FROM t WHERE name = '?' AND id = ?":       "SELECT * FROM t WHERE name = '?' AND id = $1",
		"SELECT * FROM t WHERE name = 'it''s?' AND id = ?":  "SELECT * FROM t WHERE name = 'it''s?' AND id = $1",
		`SELECT "weird?col" FROM t WHERE id = ?`:            `SELECT "weird?col" FROM t WHERE id = $1`,
		"SELECT * FROM t -- comment ?\nWHERE id = ?":        "SELECT * FROM t -- comment ?\nWHERE id = $1",
		"SELECT * FROM t /* ? */ WHERE id = ?":              "SELECT * FROM t /* ? */ WHERE id = $1",
		"SELECT * FROM t":                                   "SELECT * FROM t",
		"UPDATE t SET a = ? WHERE b = ? AND c = 'x?y'":      "UPDATE t SET a = $1 WHERE b = $2 AND c = 'x?y'",
		"SELECT * FROM t WHERE a = ? AND b = '?' AND c = ?": "SELECT * FROM t WHERE a = $1 AND b = '?' AND c = $2",
	}
	for input, want := range cases {
		if got := rebindDollarPlaceholders(input); got != want {
			t.Fatalf("rebindDollarPlaceholders(%q) = %q; want %q", input, got, want)
		}
	}
}

func TestDialectHelpers(t *testing.T) {
	sqlite := Dialect{backend: BackendSQLite}
	postgres := Dialect{backend: BackendPostgres}
	if sqlite.Rebind("SELECT ?") != "SELECT ?" {
		t.Fatal("sqlite rebind must be identity")
	}
	if postgres.Rebind("SELECT ?") != "SELECT $1" {
		t.Fatal("postgres rebind must renumber")
	}
	if sqlite.ScalarMax() != "max" || postgres.ScalarMax() != "GREATEST" {
		t.Fatalf("ScalarMax = %q / %q", sqlite.ScalarMax(), postgres.ScalarMax())
	}
	if sqlite.limitOffset() != "LIMIT -1 OFFSET ?" || postgres.limitOffset() != "OFFSET ?" {
		t.Fatalf("limitOffset = %q / %q", sqlite.limitOffset(), postgres.limitOffset())
	}
	if sqlite.forUpdate() != "" || postgres.forUpdate() != " FOR UPDATE" {
		t.Fatalf("forUpdate = %q / %q", sqlite.forUpdate(), postgres.forUpdate())
	}
	if sqlite.binaryType() != "BLOB" || postgres.binaryType() != "BYTEA" {
		t.Fatalf("binaryType = %q / %q", sqlite.binaryType(), postgres.binaryType())
	}
	if sqlite.offsetColumn() != "offset" || postgres.offsetColumn() != `"offset"` {
		t.Fatalf("offsetColumn = %q / %q", sqlite.offsetColumn(), postgres.offsetColumn())
	}
	if sqlite.Backend() != BackendSQLite || postgres.Backend() != BackendPostgres {
		t.Fatal("Backend() mismatch")
	}
}

func TestPostgresErrorClassification(t *testing.T) {
	for code, want := range map[string]bool{
		"23505": true,
		"40001": false,
		"40P01": false,
		"42P01": false,
	} {
		err := &pgconn.PgError{Code: code, Message: "test"}
		if got := IsUniqueErr(err); got != want {
			t.Fatalf("IsUniqueErr(%s) = %v; want %v", code, got, want)
		}
	}
	for code, want := range map[string]bool{
		"40001": true,
		"40P01": true,
		"23505": false,
		"08006": false,
	} {
		err := &pgconn.PgError{Code: code, Message: "test"}
		if got := isBusyErr(err); got != want {
			t.Fatalf("isBusyErr(%s) = %v; want %v", code, got, want)
		}
	}
	if IsUniqueErr(nil) || isBusyErr(nil) {
		t.Fatal("nil error must not classify")
	}
	if IsUniqueErr(context.DeadlineExceeded) || isBusyErr(context.DeadlineExceeded) {
		t.Fatal("non-driver error must not classify")
	}
}

func TestPostgresMigrationsMatchRootVersions(t *testing.T) {
	root, err := loadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := fs.Sub(migrations.PostgresFiles, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	pg, err := loadMigrations(sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(root) != len(pg) {
		t.Fatalf("postgres migration count = %d; want %d", len(pg), len(root))
	}
	for i := range root {
		if root[i].version != pg[i].version || root[i].description != pg[i].description {
			t.Fatalf("migration %d = %s; postgres %d = %s", root[i].version, root[i].description, pg[i].version, pg[i].description)
		}
	}
	for _, m := range pg {
		if strings.Contains(strings.ToUpper(string(m.sql)), "PRAGMA") {
			t.Fatalf("postgres migration %d contains PRAGMA", m.version)
		}
	}
}
