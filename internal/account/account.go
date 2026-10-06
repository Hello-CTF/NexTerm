package account

import (
	"database/sql"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type Accounts struct {
	db  *sql.DB
	now func() int64
}

type Option func(*Accounts)

func WithNow(now func() int64) Option {
	return func(a *Accounts) {
		if now != nil {
			a.now = now
		}
	}
}

func New(db *sql.DB, options ...Option) *Accounts {
	a := &Accounts{db: db, now: ids.NowMS}
	for _, option := range options {
		option(a)
	}
	return a
}

func dbError(err error) error {
	return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
}

func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT &&
		(sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY)
}

func translateUserWriteError(err error) error {
	if isUniqueViolation(err) {
		return ipc.NewError(ipc.CodeBadParam, "参数错误: 用户名已存在")
	}
	return dbError(err)
}
