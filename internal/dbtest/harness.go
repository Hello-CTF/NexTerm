package dbtest

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const PostgresDSNEnv = "NEXTERM_TEST_PG_DSN"

type Fixture struct {
	DSN    string
	Schema string
}

func NewFixture(t *testing.T) *Fixture {
	t.Helper()
	dsn := os.Getenv(PostgresDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a real PostgreSQL DSN to run these tests", PostgresDSNEnv)
	}
	schema := "nexterm_test_" + strings.ToLower(ids.New())
	admin := openRaw(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = admin.Close()
	})
	return &Fixture{DSN: withSearchPath(dsn, schema), Schema: schema}
}

func (f *Fixture) OpenStore(t *testing.T) *store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := store.OpenBackend(ctx, store.BackendConfig{Backend: store.BackendPostgres, DSN: f.DSN}, store.OpenOptions{})
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (f *Fixture) OpenRaw(t *testing.T) *sql.DB {
	t.Helper()
	db := openRaw(t, f.DSN)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func openRaw(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres connection: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("postgres is not reachable via %s: %v", PostgresDSNEnv, err)
	}
	return db
}

func withSearchPath(dsn, schema string) string {
	if strings.Contains(dsn, "://") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		return dsn + separator + "search_path=" + schema
	}
	return dsn + " search_path=" + schema
}
