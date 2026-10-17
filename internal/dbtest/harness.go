package dbtest

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	mysqldriver "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Fixture struct {
	DSN     string
	Schema  string
	Backend store.Backend
}

func NewFixture(t *testing.T) *Fixture {
	t.Helper()
	backend, err := store.ParseBackend(os.Getenv("NEXTERM_TEST_DB_BACKEND"))
	if err != nil {
		t.Fatal(err)
	}
	dsn := os.Getenv("NEXTERM_TEST_DB_DSN")
	if backend == store.BackendSQLite {
		return &Fixture{Backend: backend, DSN: filepath.Join(t.TempDir(), "contract.db")}
	}
	if dsn == "" {
		t.Fatal("NEXTERM_TEST_DB_DSN is required for the selected database backend")
	}
	name := "nexterm_test_" + strings.ToLower(ids.New())
	fixture := &Fixture{Backend: backend, Schema: name}
	driver := "pgx"
	if backend == store.BackendPostgres {
		if strings.Contains(dsn, "://") {
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			q.Set("search_path", name)
			u.RawQuery = q.Encode()
			fixture.DSN = u.String()
		} else {
			fixture.DSN = dsn + " search_path=" + name
		}
	} else {
		driver = "mysql"
		config, err := mysqldriver.ParseDSN(dsn)
		if err != nil {
			t.Fatal(err)
		}
		config.DBName = name
		fixture.DSN = config.FormatDSN()
	}
	admin, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	create := "CREATE SCHEMA " + name
	drop := "DROP SCHEMA " + name + " CASCADE"
	if driver == "mysql" {
		create = "CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
		drop = "DROP DATABASE " + name
	}
	if _, err := admin.ExecContext(ctx, create); err != nil {
		_ = admin.Close()
		t.Fatalf("create isolated database namespace: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, drop); err != nil {
			t.Errorf("drop isolated database namespace: %v", err)
		}
		_ = admin.Close()
	})
	return fixture
}
func (f *Fixture) OpenStore(t *testing.T) *store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	db, err := store.OpenBackend(ctx, store.BackendConfig{Backend: f.Backend, DSN: f.DSN}, store.OpenOptions{})
	if err != nil {
		t.Fatalf("open %s store: %v", f.Backend, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func (f *Fixture) OpenRaw(t *testing.T) *sql.DB { t.Helper(); return f.OpenStore(t).DB() }
