package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
	row := KnownHostRow{ID: ids.New(), Host: host, Port: port, KeyType: keyType, Fingerprint: fingerprint, AddedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}
	err := knownHost.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "owner_id"}, {Name: "host"}, {Name: "port"}, {Name: "key_type"}},
			DoUpdates: clause.Set{
				{Column: clause.Column{Name: "key"}, Value: knownHostKeyAssignment{}},
				{Column: clause.Column{Name: "fingerprint"}, Value: incomingValue{name: "fingerprint"}},
				{Column: clause.Column{Name: "added_at"}, Value: incomingValue{name: "added_at"}},
			},
			Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
		}).
		Create(&row)
	if err != nil {
		return dbError(err)
	}
	return nil
}

// KnownHostFillKeyMaterial completes recorded key material for an already trusted endpoint without touching
// the trust revision. The empty-material guard keeps it idempotent.
func (s *Store) KnownHostFillKeyMaterial(ctx context.Context, host string, port int32, keyType, fingerprint, key string) error {
	knownHost := query.Use(s.db).KnownHostRow
	if _, err := knownHost.WithContext(ctx).
		Where(knownHost.Host.Eq(host), knownHost.Port.Eq(port), knownHost.KeyType.Eq(keyType), knownHost.Fingerprint.Eq(fingerprint), knownHost.Key.Eq("")).
		Where(ownerConditions(ctx, knownHost.TableName())...).
		Updates(map[string]any{"key": key}); err != nil {
		return dbError(err)
	}
	return nil
}

// KnownHostAcceptWithKey records trust together with the complete public key, so verification can exact-match
// from the ledger without consulting a mirror file.
func (s *Store) KnownHostAcceptWithKey(ctx context.Context, host string, port int32, keyType, fingerprint, key string) error {
	knownHost := query.Use(s.db).KnownHostRow
	row := KnownHostRow{ID: ids.New(), Host: host, Port: port, KeyType: keyType, Fingerprint: fingerprint, Key: key, AddedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}
	err := knownHost.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "owner_id"}, {Name: "host"}, {Name: "port"}, {Name: "key_type"}},
			DoUpdates: clause.AssignmentColumns([]string{"fingerprint", "key", "added_at"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
		}).
		Create(&row)
	if err != nil {
		return dbError(err)
	}
	return nil
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

// KnownHostCompensateAccept rolls back a ledger accept whose mirror write failed. A nil previous removes the
// freshly inserted row; otherwise the prior fingerprint and revision are restored in place.
func (s *Store) KnownHostCompensateAccept(ctx context.Context, host string, port int32, keyType string, previous *KnownHostRow) error {
	knownHost := query.Use(s.db).KnownHostRow
	scoped := knownHost.WithContext(ctx).
		Where(knownHost.Host.Eq(host), knownHost.Port.Eq(port), knownHost.KeyType.Eq(keyType)).
		Where(ownerConditions(ctx, knownHost.TableName())...)
	if previous == nil {
		if _, err := scoped.Delete(); err != nil {
			return dbError(err)
		}
		return nil
	}
	if _, err := scoped.Updates(map[string]any{"fingerprint": previous.Fingerprint, "key": previous.Key, "added_at": previous.AddedAt}); err != nil {
		return dbError(err)
	}
	return nil
}

// KnownHostRemoveForMirror removes one owner-scoped ledger row together with its sync tombstone in a single
// transaction and returns the removed row so a failed mirror rewrite can restore it.
func (s *Store) KnownHostRemoveForMirror(ctx context.Context, id string) (KnownHostRow, bool, error) {
	var removed KnownHostRow
	found := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		knownHost := queries.KnownHostRow
		row, err := knownHost.WithContext(ctx).
			Where(knownHost.ID.Eq(id)).
			Where(ownerConditions(ctx, knownHost.TableName())...).
			First()
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
	if err != nil {
		return KnownHostRow{}, false, dbError(err)
	}
	return removed, found, nil
}

// KnownHostRestoreRemoved re-inserts a ledger row removed by KnownHostRemoveForMirror and drops its sync
// tombstone, compensating a mirror rewrite that failed after the removal committed.
func (s *Store) KnownHostRestoreRemoved(ctx context.Context, row KnownHostRow) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
		tombstone := query.Use(tx).SyncTombstone
		if _, err := tombstone.WithContext(ctx).
			Where(tombstone.ID.Eq(row.ID), tombstone.OwnerID.Eq(row.OwnerID)).
			Delete(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}
