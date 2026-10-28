package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

// SyncTombstoneKind marks a deleted memory entry in the sync tombstone table.
const SyncTombstoneKind = "memory"

// ErrSyncConflict reports that an entry changed after the caller read it;
// the synced write was not applied.
var ErrSyncConflict = errors.New("semantic memory changed during sync")

// Entries returns every entry of scope ordered by ID.
func (s *Store) Entries(ctx context.Context, scope Scope) ([]Entry, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	var rows []entryRow
	if err := s.db.WithContext(ctx).
		Where("tenant = ? AND subject = ?", scope.Tenant, scope.Subject).
		Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list semantic memory: %w", err)
	}
	entries := make([]Entry, len(rows))
	for i, row := range rows {
		entries[i] = entryFromRow(row)
	}
	return entries, nil
}

// PutSynced writes an entry received through sync with its peer revision
// when the scope's entry still equals expected (nil: no entry). Topic and
// content pass the write-time validation and the content is redacted
// again, so a secret never lands even from a peer with older rules. The local version counter advances on every write.
func (s *Store) PutSynced(ctx context.Context, scope Scope, entry Entry, expected *Entry) error {
	scope, err := normalizeScope(scope)
	if err != nil {
		return err
	}
	if err := validateID(entry.ID); err != nil {
		return err
	}
	topic, err := normalizeTopic(entry.Topic)
	if err != nil {
		return err
	}
	if topic != entry.Topic {
		return fmt.Errorf("%w: topic is not normalized", ErrInvalidInput)
	}
	if err := validateContent(entry.Content); err != nil {
		return err
	}
	content, redacted := RedactText(entry.Content)
	redacted = redacted || entry.Redacted
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, owner, err := loadEntry(ctx, tx, entry.ID)
		found := err == nil
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if found && owner != scope {
			return ErrPermissionDenied
		}
		if found != (expected != nil) || found && (current.Version != expected.Version || current.UpdatedAt != expected.UpdatedAt) {
			return ErrSyncConflict
		}
		if !found {
			return tx.Create(&entryRow{
				ID: entry.ID, Tenant: scope.Tenant, Subject: scope.Subject, Topic: topic, Content: content,
				Version: 1, Redacted: redacted, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
			}).Error
		}
		result := tx.Model(&entryRow{}).
			Where("id = ? AND tenant = ? AND subject = ? AND version = ?", entry.ID, scope.Tenant, scope.Subject, current.Version).
			Updates(map[string]any{
				"topic": topic, "content": content, "redacted": redacted, "version": int64(current.Version + 1),
				"created_at": entry.CreatedAt, "updated_at": entry.UpdatedAt,
			})
		if result.Error != nil {
			return fmt.Errorf("write synced semantic memory: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrSyncConflict
		}
		return nil
	})
}

// DeleteSynced applies a synced deletion: the scope's entry is removed
// unless it changed after deletedAt. It reports whether the deletion took
// effect (an absent entry counts as removed).
func (s *Store) DeleteSynced(ctx context.Context, scope Scope, id string, deletedAt int64) (bool, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return false, err
	}
	if err := validateID(id); err != nil {
		return false, err
	}
	applied := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, owner, err := loadEntry(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			applied = true
			return nil
		}
		if err != nil {
			return err
		}
		if owner != scope {
			return ErrPermissionDenied
		}
		if current.UpdatedAt > deletedAt {
			return nil
		}
		if err := tx.Where("id = ? AND tenant = ? AND subject = ?", id, scope.Tenant, scope.Subject).Delete(&entryRow{}).Error; err != nil {
			return fmt.Errorf("delete synced semantic memory: %w", err)
		}
		applied = true
		return nil
	})
	return applied, err
}

// recordDeleteTombstone lets a user deletion reach the owner's other
// devices. A store with its own database has no sync tables.
func (s *Store) recordDeleteTombstone(ctx context.Context, tx *gorm.DB, id string) error {
	if s.ownsDB {
		return nil
	}
	return store.RecordSyncTombstoneTx(ctx, tx, id, SyncTombstoneKind, ids.NowMS())
}
