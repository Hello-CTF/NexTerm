package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/db/sqlitedriver"
)

// SQLiteSnapshot describes a database copy written by SnapshotSQLite.
type SQLiteSnapshot struct {
	SQLiteVersion string
	// SchemaFingerprint hashes the schema SQL of every table and index, so two
	// snapshots can be compared without opening them.
	SchemaFingerprint string
}

// SnapshotSQLite writes a transactionally consistent copy of the SQLite
// database at source to dest with VACUUM INTO. It reads the source through a
// read-only connection, so it is safe while a server holds the database open
// in WAL mode: the copy contains every transaction committed before it started
// and nothing written afterwards. dest must not exist. The copy is switched to
// rollback-journal mode so it is a single self-contained file, and it is
// verified with integrity_check before SnapshotSQLite returns.
func SnapshotSQLite(ctx context.Context, source, dest string) (SQLiteSnapshot, error) {
	info, err := os.Stat(source)
	if err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("SQLite database %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return SQLiteSnapshot{}, fmt.Errorf("SQLite database %s is not a regular file", source)
	}
	if _, err := os.Lstat(dest); err == nil {
		return SQLiteSnapshot{}, fmt.Errorf("snapshot target %s already exists", dest)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return SQLiteSnapshot{}, err
	}
	created := sidecarsCreatedBy(source)
	err = vacuumInto(ctx, source, dest)
	if fixErr := created.restoreOwnership(source); fixErr != nil && err == nil {
		err = fixErr
	}
	if err != nil {
		_ = os.Remove(dest)
		return SQLiteSnapshot{}, err
	}
	snapshot, err := verifySnapshot(ctx, dest)
	if err != nil {
		_ = os.Remove(dest)
		_ = os.Remove(dest + "-journal")
		return SQLiteSnapshot{}, err
	}
	return snapshot, nil
}

func vacuumInto(ctx context.Context, source, dest string) error {
	u := sqliteURL(source)
	u.RawQuery = fmt.Sprintf("mode=ro&_pragma=busy_timeout(%d)", defaultBusyTimeoutMS)
	db, err := sql.Open(sqlitedriver.Name, u.String())
	if err != nil {
		return fmt.Errorf("open SQLite database %s: %w", source, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return fmt.Errorf("copy SQLite database %s: %w", source, err)
	}
	return nil
}

func verifySnapshot(ctx context.Context, path string) (SQLiteSnapshot, error) {
	u := sqliteURL(path)
	u.RawQuery = "mode=rw"
	db, err := sql.Open(sqlitedriver.Name, u.String())
	if err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("open snapshot: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("set snapshot journal mode: %w", err)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("check snapshot integrity: %w", err)
	}
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			_ = rows.Close()
			return SQLiteSnapshot{}, fmt.Errorf("check snapshot integrity: %w", err)
		}
		problems = append(problems, line)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("check snapshot integrity: %w", err)
	}
	if len(problems) != 1 || problems[0] != "ok" {
		return SQLiteSnapshot{}, fmt.Errorf("snapshot failed integrity_check: %s", strings.Join(problems, "; "))
	}
	var snapshot SQLiteSnapshot
	if err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&snapshot.SQLiteVersion); err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("read SQLite version: %w", err)
	}
	schema, err := db.QueryContext(ctx, "SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite_%' ORDER BY type, name")
	if err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("read snapshot schema: %w", err)
	}
	hash := sha256.New()
	for schema.Next() {
		var kind, name, statement string
		if err := schema.Scan(&kind, &name, &statement); err != nil {
			_ = schema.Close()
			return SQLiteSnapshot{}, fmt.Errorf("read snapshot schema: %w", err)
		}
		fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", kind, name, statement)
	}
	if err := errors.Join(schema.Err(), schema.Close()); err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("read snapshot schema: %w", err)
	}
	snapshot.SchemaFingerprint = hex.EncodeToString(hash.Sum(nil))
	return snapshot, nil
}

// createdSidecars records which WAL sidecar files did not exist before the
// snapshot opened the source. SQLite may create them even for a read-only
// connection; when the snapshot runs as root against a stopped server they
// must keep the database owner, or the service could no longer open them.
type createdSidecars []string

func sidecarsCreatedBy(source string) createdSidecars {
	var missing createdSidecars
	for _, name := range []string{source + "-wal", source + "-shm"} {
		if _, err := os.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, name)
		}
	}
	return missing
}

func (c createdSidecars) restoreOwnership(source string) error {
	for _, name := range c {
		if _, err := os.Lstat(name); err != nil {
			continue
		}
		if err := matchOwner(name, source); err != nil {
			return fmt.Errorf("restore owner of %s: %w", name, err)
		}
		if ownerOnlyPermissionsSupported() {
			if err := os.Chmod(name, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("harden %s permissions: %w", name, err)
			}
		}
	}
	return nil
}
