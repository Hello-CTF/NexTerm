package store

import "context"

func (s *Store) ScrubFreeSpace(ctx context.Context) error {
	var busy, logPages, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointed); err != nil {
		return dbError(err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return dbError(err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointed); err != nil {
		return dbError(err)
	}
	return nil
}
