package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

func acquireLegacyMigrationLock(ctx context.Context, path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	lock := newLegacyMigrationFileLock(file)
	for {
		locked, err := lock.tryLock()
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return lock.close, nil
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func closeLegacyMigrationFile(file *os.File, unlock func() error) error {
	return errors.Join(unlock(), file.Close())
}

func legacyMigrationLockError(err error) error {
	return fmt.Errorf("legacy migration file lock: %w", err)
}
