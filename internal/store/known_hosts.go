package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) KnownHostGet(ctx context.Context, host string, port int32, keyType string) (KnownHostRow, bool, error) {
	knownHost := query.Use(s.db).KnownHostRow
	row, err := knownHost.WithContext(ctx).
		Where(knownHost.Host.Eq(host), knownHost.Port.Eq(port), knownHost.KeyType.Eq(keyType)).
		Where(ownerConditions(ctx, knownHost.TableName())...).
		First()
	if isNoRows(err) {
		return KnownHostRow{}, false, nil
	}
	if err != nil {
		return KnownHostRow{}, false, dbError(err)
	}
	return *row, true, nil
}

// knownHostKeyAssignment keeps recorded key material when a fingerprint-only accept re-confirms the same
// fingerprint and clears it when the fingerprint changes, so stale material never verifies under a new one.
// The key assignment must precede the fingerprint assignment: MySQL-family upserts evaluate assignments left
// to right against the partially updated row.
type knownHostKeyAssignment struct{}

func (knownHostKeyAssignment) Build(builder clause.Builder) {
	// PostgreSQL and SQLite read the current row through the table-qualified name; MySQL-family upserts
	// resolve the table-qualified name to the proposed row, so they need the unqualified reference.
	table := "known_host"
	if statement, ok := builder.(*gorm.Statement); ok && statement.Dialector.Name() == "mysql" {
		table = ""
	}
	builder.WriteString("CASE WHEN ")
	incomingValue{name: "fingerprint"}.Build(builder)
	builder.WriteString(" <> ")
	builder.WriteQuoted(clause.Column{Table: table, Name: "fingerprint"})
	builder.WriteString(" THEN '' ELSE ")
	builder.WriteQuoted(clause.Column{Table: table, Name: "key"})
	builder.WriteString(" END")
}

// KnownHostAccept records a fingerprint-only trust decision. Any key material recorded for the endpoint is
// kept while the fingerprint is unchanged and cleared when the fingerprint changes; the material is filled
// in by the next acceptance that carries the complete public key.
func (s *Store) KnownHostAccept(ctx context.Context, host string, port int32, keyType, fingerprint string) error {
	knownHost := query.Use(s.db).KnownHostRow
	row := KnownHostRow{ID: ids.New(), Host: host, Port: port, KeyType: keyType, Fingerprint: fingerprint, OpToken: ids.New(), AddedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}
	err := inBusyRetry(func() error {
		return knownHost.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "owner_id"}, {Name: "host"}, {Name: "port"}, {Name: "key_type"}},
				DoUpdates: clause.Set{
					{Column: clause.Column{Name: "key"}, Value: knownHostKeyAssignment{}},
					{Column: clause.Column{Name: "fingerprint"}, Value: incomingValue{name: "fingerprint"}},
					{Column: clause.Column{Name: "op_token"}, Value: incomingValue{name: "op_token"}},
					{Column: clause.Column{Name: "added_at"}, Value: incomingValue{name: "added_at"}},
				},
				Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
			}).
			Create(&row)
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

// ErrKnownHostConflict reports an accept that would replace a trusted key without the replace flag.
var ErrKnownHostConflict = errors.New("SSH host key store conflict")

// inBusyRetry runs fn, retrying serialization failures (SQLite busy, PostgreSQL
// serialization/deadlock, MySQL/MariaDB lock timeout and deadlock) a bounded number of times. Every
// caller's transaction is guarded by conditional writes, so a retry never reapplies a stale outcome.
func inBusyRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = fn(); !isBusyErr(err) {
			return err
		}
	}
	return err
}

// KnownHostAcceptForMirror atomically reads the current row and applies the accept in one transaction,
// returning the exact accepted outcome and the actual previous row (nil when none existed). The caller
// uses the returned pair for mirror-failure compensation: the accepted outcome as the CAS identity and
// the actual previous as the only legitimate rollback target, so an interleaved independent decision can
// never be rolled back by mistake. Without replace, an existing row with a different fingerprint fails
// with ErrKnownHostConflict.
func (s *Store) KnownHostAcceptForMirror(ctx context.Context, host string, port int32, keyType, fingerprint, key string, replace bool) (KnownHostRow, *KnownHostRow, error) {
	var accepted KnownHostRow
	var previous *KnownHostRow
	err := inBusyRetry(func() error {
		// Each retry is a new transaction: the returned previous may only come from the transaction
		// that actually succeeded, never from a rolled-back one.
		previous = nil
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			queries := query.Use(tx)
			knownHost := queries.KnownHostRow
			// Insert-if-absent first: ON CONFLICT DO NOTHING waits out any concurrent inserter, so a row
			// read afterwards is the definitive state at write time — an empty endpoint can never hide a
			// committed row from the conflict check or from the previous snapshot.
			for attempt := 0; attempt < 3; attempt++ {
				accepted = KnownHostRow{ID: ids.New(), Host: host, Port: port, KeyType: keyType, Fingerprint: fingerprint, Key: key, OpToken: ids.New(), AddedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}
				if err := knownHost.WithContext(ctx).
					Clauses(clause.OnConflict{
						Columns:   []clause.Column{{Name: "owner_id"}, {Name: "host"}, {Name: "port"}, {Name: "key_type"}},
						DoNothing: true,
					}).
					Create(&accepted); err != nil {
					return err
				}
				scope := knownHost.WithContext(ctx).
					Where(knownHost.Host.Eq(host), knownHost.Port.Eq(port), knownHost.KeyType.Eq(keyType)).
					Where(ownerConditions(ctx, knownHost.TableName())...)
				var row *model.KnownHostRow
				var err error
				if tx.Dialector.Name() == "postgres" || tx.Dialector.Name() == "mysql" {
					row, err = scope.Clauses(clause.Locking{Strength: "UPDATE"}).First()
				} else {
					row, err = scope.First()
				}
				if isNoRows(err) {
					continue
				}
				if err != nil {
					return err
				}
				if row.ID == accepted.ID {
					return nil
				}
				previousRow := *row
				previous = &previousRow
				if !replace && previousRow.Fingerprint != fingerprint {
					return ErrKnownHostConflict
				}
				updated, err := knownHost.WithContext(ctx).
					Where(knownHost.ID.Eq(row.ID)).
					Where(ownerConditions(ctx, knownHost.TableName())...).
					Updates(map[string]any{"fingerprint": fingerprint, "key": key, "op_token": accepted.OpToken, "added_at": accepted.AddedAt})
				if err != nil {
					return err
				}
				if updated.RowsAffected == 0 {
					return fmt.Errorf("known host row vanished during accept")
				}
				return nil
			}
			return fmt.Errorf("known host accept kept racing a concurrent writer")
		})
	})
	if err != nil {
		return KnownHostRow{}, nil, dbError(err)
	}
	return accepted, previous, nil
}

// KnownHostFillKeyMaterial completes recorded key material for an already trusted endpoint without touching
// the trust revision. The empty-material guard keeps it idempotent.
func (s *Store) KnownHostFillKeyMaterial(ctx context.Context, host string, port int32, keyType, fingerprint, key string) error {
	knownHost := query.Use(s.db).KnownHostRow
	err := inBusyRetry(func() error {
		_, err := knownHost.WithContext(ctx).
			Where(knownHost.Host.Eq(host), knownHost.Port.Eq(port), knownHost.KeyType.Eq(keyType), knownHost.Fingerprint.Eq(fingerprint), knownHost.Key.Eq("")).
			Where(ownerConditions(ctx, knownHost.TableName())...).
			Updates(map[string]any{"key": key})
		return err
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

// KnownHostAcceptWithKey records trust together with the complete public key, so verification can exact-match
// from the ledger without consulting a mirror file. It returns the exact outcome of this accept (fingerprint,
// key material and revision); the ID is only meaningful for a fresh insert. Callers must use the returned
// outcome — not a re-read, which could observe an interleaved writer — as the identity of this write for
// compensation purposes.
func (s *Store) KnownHostAcceptWithKey(ctx context.Context, host string, port int32, keyType, fingerprint, key string) (KnownHostRow, error) {
	knownHost := query.Use(s.db).KnownHostRow
	row := KnownHostRow{ID: ids.New(), Host: host, Port: port, KeyType: keyType, Fingerprint: fingerprint, Key: key, OpToken: ids.New(), AddedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}
	err := inBusyRetry(func() error {
		return knownHost.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "owner_id"}, {Name: "host"}, {Name: "port"}, {Name: "key_type"}},
				DoUpdates: clause.AssignmentColumns([]string{"fingerprint", "key", "op_token", "added_at"}),
				Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
			}).
			Create(&row)
	})
	if err != nil {
		return KnownHostRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) KnownHostList(ctx context.Context) ([]KnownHostRow, error) {
	knownHost := query.Use(s.db).KnownHostRow
	result := []KnownHostRow{}
	err := knownHost.WithContext(ctx).
		Where(ownerConditions(ctx, knownHost.TableName())...).
		Order(knownHost.Host, knownHost.Port).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

// KnownHostListForMigration is restricted to startup migration, which must map legacy file entries to every explicitly owning ledger row.
func (s *Store) KnownHostListForMigration(ctx context.Context) ([]KnownHostRow, error) {
	knownHost := query.Use(s.db).KnownHostRow
	result := []KnownHostRow{}
	err := knownHost.WithContext(ctx).
		Order(knownHost.OwnerID, knownHost.Host, knownHost.Port, knownHost.KeyType, knownHost.Fingerprint, knownHost.ID).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) KnownHostRemove(ctx context.Context, id string) error {
	err := inBusyRetry(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			optIn, err := s.knownHostSyncOptInTx(ctx, tx)
			if err != nil {
				return err
			}
			knownHost := query.Use(tx).KnownHostRow
			if _, err := knownHost.WithContext(ctx).
				Where(knownHost.ID.Eq(id)).
				Where(ownerConditions(ctx, knownHost.TableName())...).
				Delete(); err != nil {
				return err
			}
			if !optIn {
				return nil
			}
			return s.syncTombstonePut(ctx, tx, id, SyncTombstoneKindKnownHost, ids.NowMS())
		})
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) knownHostSyncOptInTx(ctx context.Context, tx *gorm.DB) (bool, error) {
	userID, hasIdentity := SyncOptInUserID(ctx)
	if !hasIdentity {
		return false, nil
	}
	setting := query.Use(tx).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(knownHostOptInKey(userID))).First()
	if isNoRows(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return row.Value == "1", nil
}

// KnownHostCompensateAccept rolls back a ledger accept whose mirror write failed. The rollback is
// conditional on the row still carrying this accept's operation token: a newer independent trust
// decision — even an identical fingerprint/key in the same millisecond — has a different token and is
// left untouched. Reports whether the rollback applied; a nil previous removes the freshly inserted row,
// otherwise the prior state is restored.
func (s *Store) KnownHostCompensateAccept(ctx context.Context, host string, port int32, keyType string, accepted KnownHostRow, previous *KnownHostRow) (bool, error) {
	knownHost := query.Use(s.db).KnownHostRow
	scoped := knownHost.WithContext(ctx).
		Where(knownHost.Host.Eq(host), knownHost.Port.Eq(port), knownHost.KeyType.Eq(keyType)).
		Where(knownHost.OpToken.Eq(accepted.OpToken)).
		Where(ownerConditions(ctx, knownHost.TableName())...)
	var applied bool
	err := inBusyRetry(func() error {
		if previous == nil {
			result, err := scoped.Delete()
			if err != nil {
				return err
			}
			applied = result.RowsAffected > 0
			return nil
		}
		result, err := scoped.Updates(map[string]any{"fingerprint": previous.Fingerprint, "key": previous.Key, "added_at": previous.AddedAt})
		if err != nil {
			return err
		}
		applied = result.RowsAffected > 0
		return nil
	})
	if err != nil {
		return false, dbError(err)
	}
	return applied, nil
}

// KnownHostRemoveForMirror removes one owner-scoped ledger row together with its sync tombstone in a single
// transaction and returns the removed row so a failed mirror rewrite can restore it. The row is read under
// a write lock where the backend supports it, so the returned snapshot is always the actual deleted state.
func (s *Store) KnownHostRemoveForMirror(ctx context.Context, id string) (KnownHostRow, bool, error) {
	var removed KnownHostRow
	found := false
	err := inBusyRetry(func() error {
		// Each retry is a new transaction: the returned snapshot may only come from the transaction
		// that actually committed, never from a rolled-back one.
		removed = KnownHostRow{}
		found = false
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			queries := query.Use(tx)
			knownHost := queries.KnownHostRow
			scope := knownHost.WithContext(ctx).
				Where(knownHost.ID.Eq(id)).
				Where(ownerConditions(ctx, knownHost.TableName())...)
			var row *model.KnownHostRow
			var err error
			if tx.Dialector.Name() == "postgres" || tx.Dialector.Name() == "mysql" {
				row, err = scope.Clauses(clause.Locking{Strength: "UPDATE"}).First()
			} else {
				row, err = scope.First()
			}
			if isNoRows(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if _, err := knownHost.WithContext(ctx).
				Where(knownHost.ID.Eq(id)).
				Where(ownerConditions(ctx, knownHost.TableName())...).
				Delete(); err != nil {
				return err
			}
			optIn, err := s.knownHostSyncOptInTx(ctx, tx)
			if err != nil {
				return err
			}
			if optIn {
				if err := s.syncTombstonePut(ctx, tx, id, SyncTombstoneKindKnownHost, ids.NowMS()); err != nil {
					return err
				}
			}
			removed = *row
			found = true
			return nil
		})
	})
	if err != nil {
		return KnownHostRow{}, false, dbError(err)
	}
	return removed, found, nil
}

// KnownHostRestoreRemoved re-inserts a ledger row removed by KnownHostRemoveForMirror and drops its sync
// tombstone, compensating a mirror rewrite that failed after the removal committed. A newer independent
// trust decision for the same endpoint wins: the removed row and its tombstone stand.
func (s *Store) KnownHostRestoreRemoved(ctx context.Context, row KnownHostRow) error {
	err := inBusyRetry(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			queries := query.Use(tx)
			knownHost := queries.KnownHostRow
			existing, err := knownHost.WithContext(ctx).
				Where(knownHost.Host.Eq(row.Host), knownHost.Port.Eq(row.Port), knownHost.KeyType.Eq(row.KeyType)).
				Where(ownerConditions(ctx, knownHost.TableName())...).
				Count()
			if err != nil {
				return err
			}
			if existing > 0 {
				return nil
			}
			if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
				return err
			}
			tombstone := queries.SyncTombstone
			if _, err := tombstone.WithContext(ctx).
				Where(tombstone.ID.Eq(row.ID), tombstone.OwnerID.Eq(row.OwnerID)).
				Delete(); err != nil {
				return err
			}
			return nil
		})
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}
