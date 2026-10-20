package dbtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type fixtureUseReport struct {
	Test    string `json:"test"`
	Backend string `json:"backend"`
}

var fixtureReportMutex sync.Mutex

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

func writeFixtureUse(t *testing.T, backend store.Backend) error {
	t.Helper()
	path := os.Getenv("NEXTERM_DBTEST_REPORT")
	if path == "" {
		return nil
	}
	fixtureReportMutex.Lock()
	defer fixtureReportMutex.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open dbtest fixture report: %w", err)
	}
	report := fixtureUseReport{Test: t.Name(), Backend: string(backend)}
	if err := json.NewEncoder(file).Encode(report); err != nil {
		_ = file.Close()
		return fmt.Errorf("write dbtest fixture report: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close dbtest fixture report: %w", err)
	}
	return nil
}

func (f *Fixture) OpenStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := f.openStore(t)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func (f *Fixture) openStore(t *testing.T) (*store.Store, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	db, err := store.OpenBackend(ctx, store.BackendConfig{Backend: f.Backend, DSN: f.DSN}, store.OpenOptions{})
	if err != nil {
		return nil, fmt.Errorf("open %s store: %w", f.Backend, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := writeFixtureUse(t, f.Backend); err != nil {
		return db, err
	}
	return db, nil
}

func (f *Fixture) OpenRaw(t *testing.T) *sql.DB { t.Helper(); return f.OpenStore(t).DB() }
