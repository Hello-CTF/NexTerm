package account

import (
	"database/sql"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type Accounts struct {
	db          *sql.DB
	now         func() int64
	totpKeyPath string
}

// userTable 集中账号表名, 便于后续存储后端调整(migrations/0018_app_user.sql 由 user 改名而来)。
const userTable = "app_user"

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

func translateUserWriteError(err error) error {
	if store.IsUniqueErr(err) {
		return ipc.NewError(ipc.CodeBadParam, "参数错误: 用户名已存在")
	}
	return dbError(err)
}
