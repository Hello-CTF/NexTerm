package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	_ "modernc.org/sqlite"
)

type Store struct {
	db    *sql.DB
	now   func() int64
	newID func() string
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve semantic memory database path: %v", ErrInvalidInput, err)
	}
	path = absolute
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create semantic memory directory: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open semantic memory database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close semantic memory database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure semantic memory database: %w", err)
	}
	store, err := open(ctx, sqliteDSN(path), 8)
	if err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = store.Close()
			return nil, fmt.Errorf("secure semantic memory database files: %w", err)
		}
	}
	return store, nil
}

func open(ctx context.Context, dsn string, connections int) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open semantic memory database: %w", err)
	}
	db.SetMaxOpenConns(connections)
	db.SetMaxIdleConns(connections)
	store := &Store{db: db, now: ids.NowMS, newID: ids.New}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func sqliteDSN(path string) string {
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(path) >= 2 && path[1] == ':' {
		path = "/" + path
	}
	u := &url.URL{Scheme: "file", Path: path}
	u.RawQuery = strings.TrimPrefix(pragmaQuery(false), "&")
	return u.String()
}

func pragmaQuery(memory bool) string {
	query := "&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)&_txlock=immediate"
	if !memory {
		query += "&_pragma=journal_mode(WAL)"
	}
	return query
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read semantic memory schema version: %w", err)
	}
	if version > SchemaVersion {
		return fmt.Errorf("unsupported semantic memory schema version %d", version)
	}
	if version == SchemaVersion {
		return nil
	}
	if version != 0 {
		return fmt.Errorf("unsupported semantic memory schema version %d", version)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin semantic memory migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS memory_entry (
			id TEXT PRIMARY KEY,
			tenant TEXT NOT NULL,
			subject TEXT NOT NULL,
			topic TEXT NOT NULL,
			content TEXT NOT NULL,
			version INTEGER NOT NULL CHECK(version > 0),
			redacted INTEGER NOT NULL CHECK(redacted IN (0, 1)),
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS memory_entry_scope_topic
			ON memory_entry(tenant, subject, topic, id)`,
		`CREATE TABLE IF NOT EXISTS memory_settings (
			tenant TEXT NOT NULL,
			subject TEXT NOT NULL,
			injection_enabled INTEGER NOT NULL CHECK(injection_enabled IN (0, 1)),
			tools_enabled INTEGER NOT NULL DEFAULT 0 CHECK(tools_enabled IN (0, 1)),
			version INTEGER NOT NULL CHECK(version > 0),
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(tenant, subject)
		)`,
		fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion),
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate semantic memory schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit semantic memory migration: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
