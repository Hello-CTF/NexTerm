package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/migrations"
)

const migrationsTable = "schema_migrations"

type migration struct {
	version     int64
	description string
	sql         []byte
	checksum    []byte
}

func (s *Store) migrate(ctx context.Context) error {
	fsys, err := s.migrationsFS()
	if err != nil {
		return migrateError(err)
	}
	all, err := loadMigrations(fsys)
	if err != nil {
		return migrateError(err)
	}
	backoff := time.Millisecond
	for attempt := 0; ; attempt++ {
		err := s.migrateOnce(ctx, all)
		if !isBusyErr(err) || attempt == 39 {
			return err
		}
		select {
		case <-ctx.Done():
			return migrateError(ctx.Err())
		case <-time.After(backoff):
		}
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

func (s *Store) migrationsFS() (fs.FS, error) {
	if s.dialect.backend == BackendPostgres {
		return fs.Sub(migrations.PostgresFiles, "postgres")
	}
	return migrations.Files, nil
}

func (s *Store) migrateOnce(ctx context.Context, all []migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return migrateError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    description TEXT NOT NULL,
    installed_on TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    checksum `+s.dialect.binaryType()+` NOT NULL
)`); err != nil {
		return migrateError(err)
	}
	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		return migrateError(err)
	}
	latest := int64(0)
	if len(all) > 0 {
		latest = all[len(all)-1].version
	}
	for version := range applied {
		if version > latest {
			return migrateError(fmt.Errorf("database migration %d is newer than this build supports (%d)", version, latest))
		}
	}

	for _, m := range all {
		checksum, exists := applied[m.version]
		if exists {
			if !bytes.Equal(checksum, m.checksum) {
				return migrateError(fmt.Errorf("migration %d (%s) does not match its applied checksum", m.version, m.description))
			}
			continue
		}
		for version := range applied {
			if version > m.version {
				return migrateError(fmt.Errorf("migration %d is missing before applied migration %d", m.version, version))
			}
		}
		if err := applyMigration(ctx, tx, m); err != nil {
			return migrateError(err)
		}
		applied[m.version] = m.checksum
	}
	if err := tx.Commit(); err != nil {
		return migrateError(err)
	}
	return nil
}

func appliedMigrations(ctx context.Context, tx *sql.Tx) (map[int64][]byte, error) {
	rows, err := tx.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64][]byte)
	for rows.Next() {
		var version int64
		var checksum []byte
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		result[version] = checksum
	}
	return result, rows.Err()
}

func applyMigration(ctx context.Context, tx *sql.Tx, m migration) error {
	if _, err := tx.ExecContext(ctx, string(m.sql)); err != nil {
		return fmt.Errorf("execute migration %d (%s): %w", m.version, m.description, err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations
(version, description, checksum) VALUES (?, ?, ?)`, m.version, m.description, m.checksum)
	return err
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var all []migration
	seen := make(map[int64]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		m, err := loadMigration(fsys, entry.Name())
		if err != nil {
			return nil, err
		}
		if seen[m.version] {
			return nil, fmt.Errorf("duplicate migration version %d", m.version)
		}
		seen[m.version] = true
		all = append(all, m)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].version < all[j].version })
	return all, nil
}

func loadMigration(fsys fs.FS, name string) (migration, error) {
	parts := strings.SplitN(strings.TrimSuffix(name, ".sql"), "_", 2)
	if len(parts) != 2 {
		return migration{}, fmt.Errorf("invalid migration filename %q", name)
	}
	version, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || version <= 0 {
		return migration{}, fmt.Errorf("invalid migration version in %q", name)
	}
	contents, err := fs.ReadFile(fsys, name)
	if err != nil {
		return migration{}, err
	}
	checksum := sha256.Sum256(contents)
	description := strings.ReplaceAll(parts[1], "_", " ")
	return migration{
		version: version, description: description, sql: contents, checksum: checksum[:],
	}, nil
}
