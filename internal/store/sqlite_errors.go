package store

import (
	"errors"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"

	sqlite "github.com/glebarez/go-sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func IsUniqueErr(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return pgErr.Code == "23505"
		}
		var mysqlErr *mysqldriver.MySQLError
		return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
	}
	code := sqliteErr.Code()
	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

func isBusyErr(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return pgErr.Code == "40001" || pgErr.Code == "40P01"
		}
		var mysqlErr *mysqldriver.MySQLError
		return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1205 || mysqlErr.Number == 1213)
	}
	primaryCode := sqliteErr.Code() & 0xff
	return primaryCode == sqlite3.SQLITE_BUSY || primaryCode == sqlite3.SQLITE_LOCKED
}
