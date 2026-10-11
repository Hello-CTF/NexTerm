package store

import (
	"errors"
	"fmt"
	"strings"

	sqlite "github.com/glebarez/go-sqlite"
	"github.com/jackc/pgx/v5/pgconn"
	sqlite3 "modernc.org/sqlite/lib"
)

type Backend string

const (
	BackendSQLite   Backend = "sqlite"
	BackendPostgres Backend = "postgres"
)

func ParseBackend(raw string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(BackendSQLite):
		return BackendSQLite, nil
	case string(BackendPostgres), "postgresql":
		return BackendPostgres, nil
	}
	return "", fmt.Errorf("invalid database backend %q (want %q or %q)", raw, BackendSQLite, BackendPostgres)
}

type Dialect struct {
	backend Backend
}

func (d Dialect) Backend() Backend { return d.backend }

func (d Dialect) ScalarMax() string {
	if d.backend == BackendPostgres {
		return "GREATEST"
	}
	return "max"
}

func (d Dialect) offsetColumn() string {
	if d.backend == BackendPostgres {
		return `"offset"`
	}
	return "offset"
}

func IsUniqueErr(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isBusyErr(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		primaryCode := sqliteErr.Code() & 0xff
		return primaryCode == sqlite3.SQLITE_BUSY || primaryCode == sqlite3.SQLITE_LOCKED
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}
