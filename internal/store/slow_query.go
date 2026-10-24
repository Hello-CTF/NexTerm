package store

import (
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// slowQueryThreshold is the duration above which a query is recorded while a debug capture is running.
const slowQueryThreshold = 50 * time.Millisecond

const slowQueryStartKey = "nexterm:slow_query_start"

var slowQueryCapture atomic.Bool

// SetSlowQueryCapture switches slow-query recording on or off; the debug capture owns the switch.
func SetSlowQueryCapture(on bool) { slowQueryCapture.Store(on) }

// registerSlowQueryCallbacks records duration, row count, table and operation only: never SQL text or values.
func registerSlowQueryCallbacks(db *gorm.DB, logger *slog.Logger) {
	begin := func(tx *gorm.DB) {
		if slowQueryCapture.Load() {
			tx.InstanceSet(slowQueryStartKey, time.Now())
		}
	}
	finish := func(op string) func(*gorm.DB) {
		return func(tx *gorm.DB) { recordSlowQuery(tx, logger, op, slowQueryThreshold) }
	}
	cb := db.Callback()
	_ = cb.Create().Before("gorm:create").Register("nexterm:slow_begin_create", begin)
	_ = cb.Create().After("gorm:create").Register("nexterm:slow_end_create", finish("insert"))
	_ = cb.Query().Before("gorm:query").Register("nexterm:slow_begin_query", begin)
	_ = cb.Query().After("gorm:query").Register("nexterm:slow_end_query", finish("select"))
	_ = cb.Update().Before("gorm:update").Register("nexterm:slow_begin_update", begin)
	_ = cb.Update().After("gorm:update").Register("nexterm:slow_end_update", finish("update"))
	_ = cb.Delete().Before("gorm:delete").Register("nexterm:slow_begin_delete", begin)
	_ = cb.Delete().After("gorm:delete").Register("nexterm:slow_end_delete", finish("delete"))
	_ = cb.Row().Before("gorm:row").Register("nexterm:slow_begin_row", begin)
	_ = cb.Row().After("gorm:row").Register("nexterm:slow_end_row", finish("select"))
	_ = cb.Raw().Before("gorm:raw").Register("nexterm:slow_begin_raw", begin)
	_ = cb.Raw().After("gorm:raw").Register("nexterm:slow_end_raw", finish("raw"))
}

func recordSlowQuery(tx *gorm.DB, logger *slog.Logger, op string, threshold time.Duration) {
	value, ok := tx.InstanceGet(slowQueryStartKey)
	if !ok {
		return
	}
	started, ok := value.(time.Time)
	if !ok {
		return
	}
	elapsed := time.Since(started)
	if elapsed < threshold || !slowQueryCapture.Load() {
		return
	}
	logger.Debug("slow query", "elapsed_ms", elapsed.Milliseconds(), "rows", tx.RowsAffected,
		"table", safeTableName(tx.Statement.Table), "op", op)
}

// safeTableName keeps plain identifiers only, so expressions or aliases never reach the log.
func safeTableName(name string) string {
	if name == "" || len(name) > 64 {
		return ""
	}
	if strings.IndexFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) >= 0 {
		return ""
	}
	return name
}
