package store

import (
	"context"
	"errors"

	"modernc.org/sqlite"
)

var ErrScrubBusy = errors.New("数据库清理被读事务阻塞")

func IsBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code()&0xff == 5
	}
	return false
}

func (s *Store) ScrubFreeSpace(ctx context.Context) error {
	if err := s.truncateWAL(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return dbError(err)
	}
	return s.truncateWAL(ctx)
}

func (s *Store) truncateWAL(ctx context.Context) error {
	var busy, logPages, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointed); err != nil {
		return dbError(err)
	}
	if busy != 0 || logPages > 0 {
		return ErrScrubBusy
	}
	return nil
}
