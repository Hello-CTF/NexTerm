package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const CipherAES256GCM = "aes256gcm-v1"

func (s *Store) CredentialPut(ctx context.Context, input CredentialInput) (string, error) {
	return s.credentialPut(ctx, s.db, input)
}

func (s *Store) CredentialPutTx(ctx context.Context, tx *gorm.DB, input CredentialInput) (string, error) {
	return s.credentialPut(ctx, tx, input)
}

func (s *Store) credentialPut(ctx context.Context, db *gorm.DB, input CredentialInput) (string, error) {
	id := input.ID
	if id == "" {
		id = ids.New()
	}
	now := ids.NowMS()
	row := CredentialRow{
		ID: id, Name: input.Name, Kind: input.Kind, Cipher: CipherAES256GCM, Nonce: input.Nonce, Blob: input.Blob,
		KEKHint: input.KEKHint, CreatedAt: now, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx),
	}
	if err := s.upsertWithDB(ctx, db, &row, []string{"name", "kind", "cipher", "nonce", "blob", "kek_hint", "updated_at"}); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) CredentialCreateTx(ctx context.Context, tx *gorm.DB, input CredentialInput) (string, error) {
	id := input.ID
	if id == "" {
		id = ids.New()
	}
	now := ids.NowMS()
	row := CredentialRow{
		ID: id, Name: input.Name, Kind: input.Kind, Cipher: CipherAES256GCM, Nonce: input.Nonce, Blob: input.Blob,
		KEKHint: input.KEKHint, CreatedAt: now, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx),
	}
	if err := query.Use(tx).CredentialRow.WithContext(ctx).Create(&row); err != nil {
		return "", dbError(err)
	}
	return id, nil
}

func (s *Store) CredentialUpdateCAS(ctx context.Context, db *gorm.DB, id string, expectedUpdatedAt int64, input CredentialInput) error {
	values := map[string]any{
		"name": input.Name, "kind": input.Kind, "cipher": CipherAES256GCM,
		"nonce": input.Nonce, "blob": input.Blob, "kek_hint": input.KEKHint,
		"updated_at": nextWriteVersion(expectedUpdatedAt),
	}
	credential := query.Use(db).CredentialRow
	info, err := credential.WithContext(ctx).
		Where(credential.ID.Eq(id), credential.UpdatedAt.Eq(expectedUpdatedAt)).
		Where(ownerConditions(ctx, credential.TableName())...).
		UpdateColumns(values)
	if err != nil {
		return dbError(err)
	}
	if info.RowsAffected != 1 {
		return ErrWriteConflict
	}
	return nil
}

func (s *Store) CredentialGetRow(ctx context.Context, id string) (CredentialRow, error) {
	return s.credentialGetRow(ctx, s.db, id)
}

func (s *Store) CredentialGetRowTx(ctx context.Context, tx *gorm.DB, id string) (CredentialRow, error) {
	return s.credentialGetRow(ctx, tx, id)
}

func (s *Store) credentialGetRow(ctx context.Context, db *gorm.DB, id string) (CredentialRow, error) {
	credential := query.Use(db).CredentialRow
	row, err := credential.WithContext(ctx).
		Where(credential.ID.Eq(id)).
		Where(ownerConditions(ctx, credential.TableName())...).
		First()
	if isNoRows(err) {
		return CredentialRow{}, notFound("凭据")
	}
	if err != nil {
		return CredentialRow{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) CredentialList(ctx context.Context) ([]CredentialRow, error) {
	credential := query.Use(s.db).CredentialRow
	result := []CredentialRow{}
	err := credential.WithContext(ctx).
		Where(ownerConditions(ctx, credential.TableName())...).
		Order(credential.Name).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) CredentialDelete(ctx context.Context, id string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		credential := queries.CredentialRow
		if _, err := credential.WithContext(ctx).
			Where(credential.ID.Eq(id)).
			Where(ownerConditions(ctx, credential.TableName())...).
			Delete(); err != nil {
			return err
		}
		tombstone := queries.CredentialTombstone
		return tombstone.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"deleted_at"}),
				Where: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "owner_id"}, Value: ipc.OwnerID(ctx)}}},
			}).
			Create(&CredentialTombstone{ID: id, DeletedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)})
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) CredentialDeleteRow(ctx context.Context, id string) error {
	credential := query.Use(s.db).CredentialRow
	_, err := credential.WithContext(ctx).
		Where(credential.ID.Eq(id)).
		Where(ownerConditions(ctx, credential.TableName())...).
		Delete()
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) CredentialUsage(ctx context.Context, credID string) ([]AssetRef, error) {
	asset := query.Use(s.db).AssetRow
	result := []AssetRef{}
	err := asset.WithContext(ctx).
		Select(asset.ID, asset.Name, asset.Kind).
		Where(asset.CredID.Eq(credID), asset.DeletedAt.IsNull()).
		Where(ownerConditions(ctx, asset.TableName())...).
		Order(asset.Sort, asset.Name).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}
