package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store struct {
	backend         Backend
	db              *gorm.DB
	sqlDB           *sql.DB
	keepAlive       *sql.Conn
	logger          *slog.Logger
	retention       retentionState
	aiHistoryShared atomic.Bool

	// backfillCtx is cancelled on Close to stop background work (the
	// timestamp-index backfill loop); backfillWG lets Close join it, and
	// backfillMu serializes the closed flag against new work starting.
	backfillCtx    context.Context
	backfillCancel context.CancelFunc
	backfillWG     sync.WaitGroup
	backfillMu     sync.Mutex
	backfillClosed bool

	protectorMu sync.RWMutex
	protector   SecretProtector
}

type OpenOptions struct {
	Logger *slog.Logger
	// BusyTimeout overrides the SQLite busy_timeout pragma applied to every
	// pooled connection; zero keeps the 30s production default. The modernc
	// busy handler cannot be interrupted once it starts waiting — neither
	// cancelling the statement context nor closing the pool stops it — so
	// tests that close or shut down while a write may be contended set a
	// short value to bound the wait instead of weakening the default.
	BusyTimeout time.Duration
}

const defaultBusyTimeoutMS = 30000

func (o OpenOptions) busyTimeoutMS() (int64, error) {
	if o.BusyTimeout < 0 {
		return 0, badParam(fmt.Errorf("busy timeout 不能为负数"))
	}
	if o.BusyTimeout == 0 {
		return defaultBusyTimeoutMS, nil
	}
	if ms := o.BusyTimeout.Milliseconds(); ms > 0 {
		return ms, nil
	}
	return 1, nil
}

func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithOptions(ctx, path, OpenOptions{})
}

func OpenWithOptions(ctx context.Context, path string, options OpenOptions) (*Store, error) {
	if path == "" {
		return nil, badParam(fmt.Errorf("数据库路径不能为空"))
	}
	busyTimeoutMS, err := options.busyTimeoutMS()
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, ipc.WrapError(ipc.CodeIO, "创建数据库目录失败: "+err.Error(), err)
		}
	}
	if err := prepareDatabaseFile(path); err != nil {
		return nil, ipc.WrapError(ipc.CodeIO, "准备数据库文件失败: "+err.Error(), err)
	}
	legacy, err := hasLegacySchemaMarkerAtPath(ctx, path, busyTimeoutMS)
	if err != nil {
		return nil, err
	}
	if legacy {
		return nil, ipc.NewError(ipc.CodeUnsupported, "不支持直接升级 v1.0.3 之前的 pre-GORM 数据库；原数据库未修改")
	}
	var s *Store
	for attempt := 0; ; attempt++ {
		db, err := openDB(sqliteDSN(path, busyTimeoutMS), 8)
		if err == nil {
			s = newStore(db, options.Logger)
			err = s.migrate(ctx)
			if err != nil {
				_ = s.Close()
				s = nil
			}
		}
		if err == nil {
			break
		}
		if !IsBusy(err) || attempt >= 9 {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(100*(attempt+1)) * time.Millisecond):
		}
	}
	if err := hardenDatabaseFiles(path); err != nil {
		_ = s.Close()
		return nil, ipc.WrapError(ipc.CodeIO, "保护数据库文件失败: "+err.Error(), err)
	}
	return s, nil
}

func OpenInMemory(ctx context.Context) (*Store, error) {
	return OpenInMemoryWithOptions(ctx, OpenOptions{})
}

func OpenInMemoryWithOptions(ctx context.Context, options OpenOptions) (*Store, error) {
	busyTimeoutMS, err := options.busyTimeoutMS()
	if err != nil {
		return nil, err
	}
	dsn := "file:nexterm-" + ids.New() + "?mode=memory&cache=shared" + pragmaQuery(true, busyTimeoutMS)
	db, err := openDB(dsn, 2)
	if err != nil {
		return nil, err
	}
	s := newStore(db, options.Logger)
	if err := s.migrate(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	keepAlive, err := s.sqlDB.Conn(context.Background())
	if err != nil {
		_ = s.Close()
		return nil, dbError(err)
	}
	s.keepAlive = keepAlive
	return s, nil
}

func newStore(db *gorm.DB, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	_ = db.Callback().Create().Before("gorm:create").Register("nexterm:conflict_conditions", preserveConflictConditions)
	sqlDB, _ := db.DB()
	backfillCtx, backfillCancel := context.WithCancel(context.Background())
	s := &Store{db: db, sqlDB: sqlDB, logger: logger, backend: BackendSQLite, backfillCtx: backfillCtx, backfillCancel: backfillCancel}
	s.aiHistoryShared.Store(true)
	return s
}

func openDB(dsn string, maxConnections int) (*gorm.DB, error) {
	config := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	db, err := gorm.Open(sqlite.Open(dsn), config)
	if err != nil {
		return nil, dbError(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, dbError(err)
	}
	sqlDB.SetMaxOpenConns(maxConnections)
	sqlDB.SetMaxIdleConns(maxConnections)
	return db, nil
}

func sqliteDSN(path string, busyTimeoutMS int64) string {
	u := sqliteURL(path)
	u.RawQuery = strings.TrimPrefix(pragmaQuery(false, busyTimeoutMS), "&")
	return u.String()
}

func sqliteImmutableDSN(path string) string {
	u := sqliteURL(path)
	u.RawQuery = "mode=ro&immutable=1"
	return u.String()
}

func sqliteURL(path string) *url.URL {
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(path) >= 2 && path[1] == ':' {
		path = "/" + path
	}
	return &url.URL{Scheme: "file", Path: path}
}

func pragmaQuery(memory bool, busyTimeoutMS int64) string {
	query := fmt.Sprintf("&_pragma=foreign_keys(1)&_pragma=busy_timeout(%d)&_pragma=synchronous(NORMAL)&_txlock=immediate", busyTimeoutMS)
	if !memory {
		query += "&_pragma=journal_mode(WAL)"
	}
	return query
}

func hasLegacySchemaMarkerAtPath(ctx context.Context, path string, busyTimeoutMS int64) (bool, error) {
	legacy, err := hasLegacySchemaMarkerInDatabase(ctx, sqliteImmutableDSN(path))
	if err != nil || legacy {
		return legacy, err
	}
	wal, err := os.Stat(path + "-wal")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, ipc.WrapError(ipc.CodeIO, "检查旧版数据库失败: "+err.Error(), err)
	}
	if wal.Size() == 0 {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	// A disposable copy includes uncheckpointed schema changes without
	// allowing SQLite to rewrite the original database or its sidecars.
	dir, err := os.MkdirTemp("", "nexterm-legacy-marker-")
	if err != nil {
		return false, ipc.WrapError(ipc.CodeIO, "检查旧版数据库失败: "+err.Error(), err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	copyPath := filepath.Join(dir, "data.db")
	for _, suffix := range []string{"", "-wal"} {
		if err := copySQLiteFile(path+suffix, copyPath+suffix); err != nil {
			return false, ipc.WrapError(ipc.CodeIO, "检查旧版数据库失败: "+err.Error(), err)
		}
	}
	return hasLegacySchemaMarkerInDatabase(ctx, sqliteDSN(copyPath, busyTimeoutMS))
}

func hasLegacySchemaMarkerInDatabase(ctx context.Context, dsn string) (bool, error) {
	db, err := openDB(dsn, 1)
	if err != nil {
		return false, err
	}
	s := newStore(db, nil)
	defer func() { _ = s.Close() }()
	legacy, err := hasLegacySchemaMarker(ctx, s)
	if err != nil {
		return false, dbError(err)
	}
	return legacy, nil
}

func copySQLiteFile(source, destination string) error {
	from, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = from.Close() }()
	to, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(to, from); err != nil {
		_ = to.Close()
		return err
	}
	return to.Close()
}

func hasLegacySchemaMarker(ctx context.Context, s *Store) (bool, error) {
	var name string
	err := s.sqlDB.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) DB() *sql.DB {
	return s.sqlDB
}

func (s *Store) GORM() *gorm.DB {
	return s.db
}

func (s *Store) Backend() Backend { return s.backend }

func (s *Store) Close() error {
	if s.keepAlive != nil {
		_ = s.keepAlive.Close()
		s.keepAlive = nil
	}
	// Mark closed under the lifecycle lock so no new backfill work can
	// start, then cancel and join it, then close the handle.
	s.backfillMu.Lock()
	s.backfillClosed = true
	s.backfillMu.Unlock()
	s.backfillCancel()
	s.backfillWG.Wait()
	return s.sqlDB.Close()
}

func EnsureID(id string) error {
	if !ids.Valid(id) {
		return badParam(fmt.Errorf("ID 格式不正确"))
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
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return err
	}
	return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
}

func migrateError(err error) error {
	return ipc.WrapError(ipc.CodeDBMigrate, "数据库迁移错误: "+err.Error(), err)
}

func badParam(err error) error {
	return ipc.BadParam(err)
}

func notFound(what string) error {
	return ipc.NewError(ipc.CodeNotFound, what+"不存在")
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

var migrateMu sync.Mutex

func (s *Store) migrate(ctx context.Context) error {
	migrateMu.Lock()
	defer migrateMu.Unlock()
	migrate := func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(model.AllModels...); err != nil {
			return err
		}
		switch s.backend {
		case BackendSQLite:
			return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_app_user_username ON app_user(username COLLATE NOCASE)`).Error
		case BackendPostgres:
			return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_app_user_username ON app_user(lower(username))`).Error
		default:
			if tx.Migrator().HasIndex(&model.AppUser{}, "idx_app_user_username") {
				return nil
			}
			if !tx.Migrator().HasColumn(&model.AppUser{}, "username_folded") {
				if err := tx.Exec("ALTER TABLE app_user ADD COLUMN username_folded varchar(191) GENERATED ALWAYS AS (lower(username)) STORED").Error; err != nil {
					return err
				}
			}
			return tx.Exec("CREATE UNIQUE INDEX idx_app_user_username ON app_user(username_folded)").Error
		}
	}
	var err error
	if s.backend == BackendMySQL || s.backend == BackendMariaDB {
		err = migrate(s.db.WithContext(ctx).Set("gorm:table_options", "CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"))
	} else {
		err = s.db.WithContext(ctx).Transaction(migrate)
	}
	if err != nil {
		return migrateError(err)
	}
	return nil
}
