package sync

import "github.com/Hello-CTF/NexTerm/internal/store"

// scalarMax 返回后端标量取大函数名: PostgreSQL 没有 SQLite 的标量 max(), 对应 GREATEST。
func scalarMax(backend store.Backend) string {
	if backend == store.BackendPostgres {
		return "GREATEST"
	}
	return "max"
}

// forUpdate 返回行锁子句: PostgreSQL 默认 READ COMMITTED, head/seq 分配必须显式锁行;
// SQLite 单写者事务天然串行, 不需要锁子句。
func forUpdate(backend store.Backend) string {
	if backend == store.BackendPostgres {
		return " FOR UPDATE"
	}
	return ""
}
