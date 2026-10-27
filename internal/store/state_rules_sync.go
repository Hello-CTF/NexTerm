package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SyncTombstoneKindStateRule marks a deleted state rule in sync_tombstone.
const SyncTombstoneKindStateRule = "state_rule"

// ErrStateRuleAssetMissing rejects a synced host-scoped rule whose host has
// not arrived on this device yet.
var ErrStateRuleAssetMissing = errors.New("store: state rule host is missing")

// ErrStateRuleAssetDeleted rejects a synced host-scoped rule whose host was
// deleted on this device; waiting cannot make it apply.
var ErrStateRuleAssetDeleted = errors.New("store: state rule host is deleted")

// ErrStateRuleLimit rejects a synced rule that would take the owner beyond
// the rule count or pattern budget; it may fit once rules are removed.
var ErrStateRuleLimit = errors.New("store: state rule limit reached")

// StateRuleGet returns the owner's rule by ID; found is false when the owner
// has no such rule.
func (s *Store) StateRuleGet(ctx context.Context, id string) (row StateRuleRow, found bool, err error) {
	rule := query.Use(s.db).StateRuleRow
	current, err := rule.WithContext(ctx).
		Where(rule.ID.Eq(id)).
		Where(ownerConditions(ctx, rule.TableName())...).
		First()
	if isNoRows(err) {
		return StateRuleRow{}, false, nil
	}
	if err != nil {
		return StateRuleRow{}, false, dbError(err)
	}
	return *current, true, nil
}

// StateRuleSyncPut writes a state rule received through sync with its peer
// revision. The write is bound to expected, the row the caller decided on
// (nil: the rule must not exist); a row that moved since reports
// ErrWriteConflict. A new rule follows the owner's other rules. A host-scoped
// rule whose host is absent reports ErrStateRuleAssetMissing. The rule's
// deletion tombstone is cleared in the same transaction.
func (s *Store) StateRuleSyncPut(ctx context.Context, input StateRuleInput, createdAt int64, expected *StateRuleRow) (StateRuleRow, error) {
	input.ID = strings.TrimSpace(input.ID)
	if err := EnsureID(input.ID); err != nil {
		return StateRuleRow{}, err
	}
	normalized, err := normalizeStateRuleInput(input, "")
	if err != nil {
		return StateRuleRow{}, err
	}
	var row StateRuleRow
	err = retryStateRulePosition(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if normalized.Scope == StateRuleScopeHost {
				asset := query.Use(tx).AssetRow
				host, err := asset.WithContext(ctx).
					Where(asset.ID.Eq(normalized.AssetID)).
					Where(ownerConditions(ctx, asset.TableName())...).
					First()
				switch {
				case isNoRows(err):
					return ErrStateRuleAssetMissing
				case err != nil:
					return err
				case host.DeletedAt != nil:
					return ErrStateRuleAssetDeleted
				}
			}
			rule := query.Use(tx).StateRuleRow
			current, err := rule.WithContext(ctx).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Where(rule.ID.Eq(normalized.ID)).
				Where(ownerConditions(ctx, rule.TableName())...).
				First()
			if isNoRows(err) {
				current = nil
			} else if err != nil {
				return err
			}
			if (current == nil) != (expected == nil) || current != nil && !sameStateRuleRow(*current, *expected) {
				return ErrWriteConflict
			}
			if err := checkStateRuleLimits(ctx, tx, []StateRuleInput{normalized}); err != nil {
				return fmt.Errorf("%w: %s", ErrStateRuleLimit, err.Error())
			}
			if current == nil {
				position, err := nextStateRulePosition(ctx, tx)
				if err != nil {
					return err
				}
				row = stateRuleRow(ctx, normalized, createdAt, normalized.UpdatedAt)
				row.Position = position
				if err := rule.WithContext(ctx).Create(&row); err != nil {
					if IsUniqueErr(err) && !isBusyErr(err) {
						taken, countErr := rule.WithContext(ctx).Where(rule.ID.Eq(normalized.ID), rule.OwnerID.Neq(ipc.OwnerID(ctx))).Count()
						if countErr == nil && taken > 0 {
							return badParam(errors.New("状态规则 ID 已被其他用户占用"))
						}
					}
					return err
				}
			} else {
				if _, err := rule.WithContext(ctx).
					Where(rule.ID.Eq(normalized.ID), rule.OwnerID.Eq(current.OwnerID), rule.UpdatedAt.Eq(current.UpdatedAt)).
					UpdateColumns(stateRuleAssignments(normalized, normalized.UpdatedAt)); err != nil {
					return err
				}
				row = stateRuleRow(ctx, normalized, current.CreatedAt, normalized.UpdatedAt)
				row.OwnerID, row.Position = current.OwnerID, current.Position
			}
			return deleteSyncTombstone(ctx, tx, normalized.ID)
		})
	})
	if err != nil {
		if errors.Is(err, ErrWriteConflict) || errors.Is(err, ErrStateRuleAssetMissing) || errors.Is(err, ErrStateRuleAssetDeleted) || errors.Is(err, ErrStateRuleLimit) {
			return StateRuleRow{}, err
		}
		return StateRuleRow{}, dbError(err)
	}
	return row, nil
}

// StateRuleSyncDelete applies a synced deletion: the owner's rule is removed
// unless it changed after deletedAt, and the tombstone is kept so the
// deletion keeps winning over older copies. It reports whether the
// tombstone took effect.
func (s *Store) StateRuleSyncDelete(ctx context.Context, id string, deletedAt int64) (bool, error) {
	applied := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rule := query.Use(tx).StateRuleRow
		current, err := rule.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(rule.ID.Eq(id)).
			Where(ownerConditions(ctx, rule.TableName())...).
			First()
		if err != nil && !isNoRows(err) {
			return err
		}
		if err == nil {
			if current.UpdatedAt > deletedAt {
				return nil
			}
			if _, err := rule.WithContext(ctx).Where(rule.ID.Eq(id), rule.OwnerID.Eq(current.OwnerID)).Delete(); err != nil {
				return err
			}
		}
		applied = true
		return s.syncTombstonePut(ctx, tx, id, SyncTombstoneKindStateRule, deletedAt)
	})
	if err != nil {
		return false, dbError(err)
	}
	return applied, nil
}

// sameStateRuleRow reports whether a rule is unchanged since the caller read
// it: state rules carry no version, so every stored field is compared.
func sameStateRuleRow(current, expected StateRuleRow) bool {
	current.OwnerID, expected.OwnerID = "", ""
	return current == expected
}

// deleteSyncTombstone clears the caller's tombstone for id: a newer live
// object replaces it.
func deleteSyncTombstone(ctx context.Context, tx *gorm.DB, id string) error {
	tombstone := query.Use(tx).SyncTombstone
	_, err := tombstone.WithContext(ctx).
		Where(tombstone.ID.Eq(id)).
		Where(ownerConditions(ctx, tombstone.TableName())...).
		Delete()
	return err
}

// RecordSyncTombstoneTx records the deletion of the caller's object id inside
// tx, for stores that keep their own rows in the application database.
func RecordSyncTombstoneTx(ctx context.Context, tx *gorm.DB, id, kind string, deletedAt int64) error {
	return (*Store)(nil).syncTombstonePut(ctx, tx, id, kind, deletedAt)
}

// recordSyncTombstoneNow records a user deletion of id at the current time.
func (s *Store) recordSyncTombstoneNow(ctx context.Context, tx *gorm.DB, id, kind string) error {
	return s.syncTombstonePut(ctx, tx, id, kind, ids.NowMS())
}
