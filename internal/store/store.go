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
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store struct {
	backend   Backend
	db        *gorm.DB
	sqlDB     *sql.DB
	keepAlive *sql.Conn
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
			return nil, ipc.WrapError(ipc.CodeIO, "创建数据库目录失败: "+err.Error(), err)
		}
	}
	if err := prepareDatabaseFile(path); err != nil {
		return nil, ipc.WrapError(ipc.CodeIO, "准备数据库文件失败: "+err.Error(), err)
	}
	var s *Store
	var legacyBackup string
	for attempt := 0; ; attempt++ {
		db, err := openDB(sqliteDSN(path), 8)
		if err == nil {
			s = newStore(db, options.Logger)
			legacy, lerr := hasLegacySchemaMarker(ctx, s)
			if lerr != nil {
				_ = s.Close()
				s = nil
				err = dbError(lerr)
			} else if legacy {
				_ = s.Close()
				s = nil
				legacyBackup, err = backupLegacyDatabase(path)
				if err != nil {
					return nil, err
				}
				continue
			} else {
				err = s.migrate(ctx)
				if err != nil {
					_ = s.Close()
					s = nil
				}
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
	if legacyBackup != "" {
		s.logger.Info("检测到旧版数据库,已备份并重建", "backup", legacyBackup)
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
	dsn := "file:nexterm-" + ids.New() + "?mode=memory&cache=shared" + pragmaQuery(true)
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
	return &Store{db: db, sqlDB: sqlDB, logger: logger, backend: BackendSQLite}
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
	query := "&_pragma=foreign_keys(1)&_pragma=busy_timeout(30000)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	if !memory {
		query += "&_pragma=journal_mode(WAL)"
	}
	return query
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

func backupLegacyDatabase(path string) (string, error) {
	backup := fmt.Sprintf("%s.legacy-%s", path, time.Now().Format("20060102-150405"))
	moved := []string{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		err := os.Rename(path+suffix, backup+suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			for i := len(moved) - 1; i >= 0; i-- {
				_ = os.Rename(backup+moved[i], path+moved[i])
			}
			return "", ipc.WrapError(ipc.CodeIO, "备份旧版数据库失败: "+err.Error(), err)
		}
		moved = append(moved, suffix)
	}
	return backup, nil
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
