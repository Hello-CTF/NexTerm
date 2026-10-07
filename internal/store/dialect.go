package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
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

func (d Dialect) Rebind(query string) string {
	if d.backend != BackendPostgres {
		return query
	}
	return rebindDollarPlaceholders(query)
}

func (d Dialect) ScalarMax() string {
	if d.backend == BackendPostgres {
		return "GREATEST"
	}
	return "max"
}

func (d Dialect) limitOffset() string {
	if d.backend == BackendPostgres {
		return "OFFSET ?"
	}
	return "LIMIT -1 OFFSET ?"
}

func (d Dialect) forUpdate() string {
	if d.backend == BackendPostgres {
		return " FOR UPDATE"
	}
	return ""
}

func (d Dialect) binaryType() string {
	if d.backend == BackendPostgres {
		return "BYTEA"
	}
	return "BLOB"
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

func rebindDollarPlaceholders(query string) string {
	var out strings.Builder
	out.Grow(len(query) + 8)
	arg := 1
	inSingleQuote := false
	inDoubleQuote := false
	inLineComment := false
	inBlockComment := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case inLineComment:
			out.WriteByte(c)
			if c == '\n' {
				inLineComment = false
			}
		case inBlockComment:
			out.WriteByte(c)
			if c == '*' && i+1 < len(query) && query[i+1] == '/' {
				out.WriteByte('/')
				i++
				inBlockComment = false
			}
		case inSingleQuote:
			out.WriteByte(c)
			if c == '\'' {
				if i+1 < len(query) && query[i+1] == '\'' {
					out.WriteByte('\'')
					i++
				} else {
					inSingleQuote = false
				}
			}
		case inDoubleQuote:
			out.WriteByte(c)
			if c == '"' {
				if i+1 < len(query) && query[i+1] == '"' {
					out.WriteByte('"')
					i++
				} else {
					inDoubleQuote = false
				}
			}
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			out.WriteByte(c)
			inLineComment = true
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			out.WriteByte(c)
			inBlockComment = true
		case c == '\'':
			out.WriteByte(c)
			inSingleQuote = true
		case c == '"':
			out.WriteByte(c)
			inDoubleQuote = true
		case c == '?':
			out.WriteString("$")
			out.WriteString(fmt.Sprint(arg))
			arg++
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}
