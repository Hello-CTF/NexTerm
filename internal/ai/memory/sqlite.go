package memory

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type entryRow struct {
	ID        string `gorm:"column:id;primaryKey"`
	Tenant    string `gorm:"column:tenant;not null;index:memory_entry_scope_topic,priority:1"`
	Subject   string `gorm:"column:subject;not null;index:memory_entry_scope_topic,priority:2"`
	Topic     string `gorm:"column:topic;not null;index:memory_entry_scope_topic,priority:3"`
	Content   string `gorm:"column:content;not null"`
	Version   uint64 `gorm:"column:version;not null"`
	Redacted  bool   `gorm:"column:redacted;not null"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
}

func (entryRow) TableName() string { return "memory_entry" }

type settingsRow struct {
	Tenant           string `gorm:"column:tenant;not null;primaryKey"`
	Subject          string `gorm:"column:subject;not null;primaryKey"`
	InjectionEnabled bool   `gorm:"column:injection_enabled;not null"`
	ToolsEnabled     bool   `gorm:"column:tools_enabled;not null;default:0"`
	Version          uint64 `gorm:"column:version;not null"`
	UpdatedAt        int64  `gorm:"column:updated_at;not null"`
}

func (settingsRow) TableName() string { return "memory_settings" }

type Store struct {
	db    *gorm.DB
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
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open semantic memory database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("open semantic memory database: %w", err)
	}
	sqlDB.SetMaxOpenConns(connections)
	sqlDB.SetMaxIdleConns(connections)
	store := &Store{db: db, now: ids.NowMS, newID: ids.New}
	if err := store.migrate(ctx); err != nil {
		_ = sqlDB.Close()
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
	u.RawQuery = "_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)&_txlock=immediate&_pragma=journal_mode(WAL)"
	return u.String()
}

func (s *Store) migrate(ctx context.Context) error {
	if err := s.db.WithContext(ctx).AutoMigrate(&entryRow{}, &settingsRow{}); err != nil {
		return fmt.Errorf("migrate semantic memory schema: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}
