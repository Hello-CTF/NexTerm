package store

import (
	"context"
	"errors"
)

var ErrScrubBusy = errors.New("数据库清理被读事务阻塞")

func IsBusy(err error) bool {
	return isBusyErr(err)
}

func (s *Store) ScrubFreeSpace(ctx context.Context) error {
	if s.dialect.backend == BackendPostgres {
		if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
			return dbError(err)
		}
		return nil
	}
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
