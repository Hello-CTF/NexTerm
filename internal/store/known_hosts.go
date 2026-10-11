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

func (s *Store) KnownHostAccept(ctx context.Context, host string, port int32, keyType, fingerprint string) error {
	knownHost := query.Use(s.db).KnownHostRow
	row := KnownHostRow{ID: ids.New(), Host: host, Port: port, KeyType: keyType, Fingerprint: fingerprint, AddedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}
	err := knownHost.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "owner_id"}, {Name: "host"}, {Name: "port"}, {Name: "key_type"}},
			DoUpdates: clause.AssignmentColumns([]string{"fingerprint", "added_at"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: s.dialect.conflictTargetColumn("known_host", "owner_id"), Value: ipc.OwnerID(ctx)}}},
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

func (s *Store) KnownHostRemove(ctx context.Context, id string) error {
	userID, hasIdentity := SyncOptInUserID(ctx)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		optIn := hasIdentity
		if hasIdentity {
			setting := queries.Setting
			row, err := setting.WithContext(ctx).Where(setting.Key.Eq(knownHostOptInKey(userID))).First()
			if err != nil && !isNoRows(err) {
				return err
			}
			if err == nil {
				optIn = row.Value == "1"
			}
		}
		knownHost := queries.KnownHostRow
		if _, err := knownHost.WithContext(ctx).
			Where(knownHost.ID.Eq(id)).
			Where(ownerConditions(ctx, knownHost.TableName())...).
			Delete(); err != nil {
			return err
		}
		if !optIn {
			return nil
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"deleted_at": gorm.Expr(s.dialect.ScalarMax() + "(sync_tombstone.deleted_at, excluded.deleted_at)"),
			}),
			Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: s.dialect.conflictTargetColumn("sync_tombstone", "owner_id"), Value: ipc.OwnerID(ctx)}}},
		}).Create(&SyncTombstone{ID: id, Kind: SyncTombstoneKindKnownHost, DeletedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}).Error
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}
