package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	_ "modernc.org/sqlite"
)

type Store struct {
	db        *sql.DB
	dialect   Dialect
	logger    *slog.Logger
	retention retentionState

	protectorMu sync.RWMutex
	protector   SecretProtector
}

type OpenOptions struct {
	Logger *slog.Logger
}

func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithOptions(ctx, path, OpenOptions{})
}

func OpenWithOptions(ctx context.Context, path string, options OpenOptions) (*Store, error) {
	if path == "" {
		return nil, badParam(fmt.Errorf("数据库路径不能为空"))
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, ipc.WrapError(ipc.CodeIO, "IO 错误: "+err.Error(), err)
		}
	}
	if err := prepareDatabaseFile(path); err != nil {
		return nil, ipc.WrapError(ipc.CodeIO, "IO 错误: "+err.Error(), err)
	}
	db, err := openDB(sqliteDSN(path), 8)
	if err != nil {
		return nil, err
	}
	s := newStore(db, options.Logger, Dialect{backend: BackendSQLite})
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := hardenDatabaseFiles(path); err != nil {
		_ = db.Close()
		return nil, ipc.WrapError(ipc.CodeIO, "IO 错误: "+err.Error(), err)
	}
	return s, nil
}

func OpenInMemory(ctx context.Context) (*Store, error) {
	return OpenInMemoryWithOptions(ctx, OpenOptions{})
}

func OpenInMemoryWithOptions(ctx context.Context, options OpenOptions) (*Store, error) {
	dsn := "file:nexterm-" + ids.New() + "?mode=memory" + pragmaQuery(true)
	db, err := openDB(dsn, 1)
	if err != nil {
		return nil, err
	}
	s := newStore(db, options.Logger, Dialect{backend: BackendSQLite})
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func newStore(db *sql.DB, logger *slog.Logger, dialect Dialect) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{db: db, dialect: dialect, logger: logger}
}

func openDB(dsn string, maxConnections int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, dbError(err)
	}
	db.SetMaxOpenConns(maxConnections)
	db.SetMaxIdleConns(maxConnections)
	return db, nil
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
	query := "&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	if !memory {
		query += "&_pragma=journal_mode(WAL)"
	}
	return query
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) Backend() Backend {
	return s.dialect.backend
}

func (s *Store) Close() error {
	return s.db.Close()
}

func EnsureID(id string) error {
	if !ids.Valid(id) {
		return badParam(fmt.Errorf("非法 ID: %s", id))
	}
	return nil
}

func ParseJSONOr(raw string) json.RawMessage {
	if !json.Valid([]byte(raw)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func dbError(err error) error {
	return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
}

func migrateError(err error) error {
	return ipc.WrapError(ipc.CodeDBMigrate, "数据库迁移错误: "+err.Error(), err)
}

func badParam(err error) error {
	return ipc.BadParam(err)
}

func notFound(what string) error {
	return ipc.NewError(ipc.CodeNotFound, "未找到: "+what)
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
