package store

import (
	"context"
	"testing"

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

func TestDialectHelpers(t *testing.T) {
	sqlite := Dialect{backend: BackendSQLite}
	postgres := Dialect{backend: BackendPostgres}
	if sqlite.ScalarMax() != "max" || postgres.ScalarMax() != "GREATEST" {
		t.Fatalf("ScalarMax = %q / %q", sqlite.ScalarMax(), postgres.ScalarMax())
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
