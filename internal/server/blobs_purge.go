package server

import (
	"errors"
	"os"
	"path/filepath"
)

// PurgeOwner removes every staged and persisted entry whose owner metadata names ownerID, and drops that
// owner's in-flight staging reservations, for an account being deleted. An empty owner is a no-op: ownerless
// entries belong to the identity-less deployment shapes, never to an account. It returns how many entries
// were removed; entries that fail to delete are reported in the joined error and the rest still go.
func (b *BlobStore) PurgeOwner(ownerID string) (int, error) {
	if b == nil || ownerID == "" {
		return 0, nil
	}
	b.persistMu.Lock()
	defer b.persistMu.Unlock()
	b.stageMu.Lock()
	defer b.stageMu.Unlock()
	for key, reservation := range b.reservations {
		if reservation.owner == ownerID {
			delete(b.reservations, key)
		}
	}
	removed := 0
	var errs []error
	for _, root := range []string{b.stageRoot(), b.keepRoot()} {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			owner, ok := stagedOwner(dir)
			if !ok || owner != ownerID {
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}
