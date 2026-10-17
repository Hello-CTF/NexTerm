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
	if err := s.truncateWAL(ctx); err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Exec("VACUUM").Error; err != nil {
		return dbError(err)
	}
	return s.truncateWAL(ctx)
}

func (s *Store) truncateWAL(ctx context.Context) error {
	var busy, logPages, checkpointed int
	if err := s.db.WithContext(ctx).Raw("PRAGMA wal_checkpoint(TRUNCATE)").Row().Scan(&busy, &logPages, &checkpointed); err != nil {
		return dbError(err)
	}
	if busy != 0 || logPages > 0 {
		return ErrScrubBusy
	}
	return nil
}
