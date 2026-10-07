package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestApplyPostgresDefaults(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://nexterm@127.0.0.1:5432/nexterm?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPostgresDefaults(config, "secret"); err != nil {
		t.Fatal(err)
	}
	if config.Password != "secret" {
		t.Fatalf("password = %q", config.Password)
	}
	for param, want := range map[string]string{
		"lock_timeout":                        "5000",
		"statement_timeout":                   "30000",
		"idle_in_transaction_session_timeout": "60000",
	} {
		if got := config.RuntimeParams[param]; got != want {
			t.Fatalf("RuntimeParams[%q] = %q; want %q", param, got, want)
		}
	}
}

func TestApplyPostgresDefaultsKeepsExplicitValues(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://nexterm@127.0.0.1:5432/nexterm?sslmode=disable&statement_timeout=90000")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPostgresDefaults(config, ""); err != nil {
		t.Fatal(err)
	}
	if got := config.RuntimeParams["statement_timeout"]; got != "90000" {
		t.Fatalf("statement_timeout = %q; want 90000", got)
	}
	if got := config.RuntimeParams["lock_timeout"]; got != "5000" {
		t.Fatalf("lock_timeout = %q; want default 5000", got)
	}
}

func TestApplyPostgresDefaultsRejectsDoublePassword(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://nexterm:inline@127.0.0.1:5432/nexterm")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPostgresDefaults(config, "file-secret"); err == nil {
		t.Fatal("DSN password + password file combination was accepted")
	}
	if err := applyPostgresDefaults(config, ""); err != nil {
		t.Fatal(err)
	}
	if config.Password != "inline" {
		t.Fatalf("password = %q", config.Password)
	}
}

func TestOpenBackendValidation(t *testing.T) {
	if _, err := OpenBackend(context.Background(), BackendConfig{Backend: "mysql"}, OpenOptions{}); err == nil {
		t.Fatal("unknown backend was accepted")
	}
	if _, err := OpenBackend(context.Background(), BackendConfig{Backend: BackendPostgres}, OpenOptions{}); err == nil {
		t.Fatal("postgres without DSN was accepted")
	}
	if _, err := OpenBackend(context.Background(), BackendConfig{Backend: BackendPostgres, DSN: "://bogus"}, OpenOptions{}); err == nil {
		t.Fatal("invalid DSN was accepted")
	}
}

func TestOpenBackendPostgresPingFailure(t *testing.T) {
	_, err := OpenBackend(context.Background(), BackendConfig{
		Backend:        BackendPostgres,
		DSN:            "postgres://nexterm@127.0.0.1:1/nexterm?sslmode=disable&connect_timeout=2",
		ConnectTimeout: 3 * time.Second,
	}, OpenOptions{})
	if err == nil {
		t.Fatal("unreachable postgres was accepted")
	}
	if !strings.Contains(err.Error(), "ping") {
		t.Fatalf("error = %v; want startup ping failure", err)
	}
}

func TestOpenBackendSQLitePassthrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := OpenBackend(context.Background(), BackendConfig{Backend: BackendSQLite, DSN: path}, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if db.Backend() != BackendSQLite {
		t.Fatalf("Backend() = %q", db.Backend())
	}
	if err := db.SettingSet(context.Background(), "backend", "sqlite"); err != nil {
		t.Fatal(err)
	}
	value, found, err := db.SettingGet(context.Background(), "backend")
	if err != nil || !found || value != "sqlite" {
		t.Fatalf("SettingGet = %q, %v, %v", value, found, err)
	}
}
