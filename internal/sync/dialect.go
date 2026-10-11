package sync

import (
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm/clause"
)

// scalarMax 返回后端标量取大函数名: PostgreSQL 没有 SQLite 的标量 max(), 对应 GREATEST。
func scalarMax(backend store.Backend) string {
	if backend == store.BackendPostgres {
		return "GREATEST"
	}
	return "max"
}

// conflictOwnerColumn 返回 upsert WHERE 里的 owner_id 列: PostgreSQL 在 ON CONFLICT DO UPDATE
// 的 WHERE 中不限定会报歧义(目标表与 excluded 伪行同名), SQLite 则禁止表名限定。
func conflictOwnerColumn(backend store.Backend, table string) clause.Column {
	if backend == store.BackendPostgres {
		return clause.Column{Table: table, Name: "owner_id"}
	}
	return clause.Column{Name: "owner_id"}
}

// forUpdate 返回行锁子句: PostgreSQL 默认 READ COMMITTED, head/seq 分配必须显式锁行;
// SQLite 单写者事务天然串行, 不需要锁子句。
func forUpdate(backend store.Backend) string {
	if backend == store.BackendPostgres {
		return " FOR UPDATE"
	}
	return ""
}
