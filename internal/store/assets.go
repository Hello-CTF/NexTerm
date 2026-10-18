package store

import (
	"context"
	"errors"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
	"gorm.io/gorm"
)

func (s *Store) AssetList(ctx context.Context, includeDeleted bool) ([]AssetRow, error) {
	asset := query.Use(s.db).AssetRow
	q := asset.WithContext(ctx).Where(ownerConditions(ctx, asset.TableName())...)
	if !includeDeleted {
		q = q.Where(asset.DeletedAt.IsNull())
	}
	result := []AssetRow{}
	if err := q.Order(asset.Sort, asset.Name).Scan(&result); err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) AssetGet(ctx context.Context, id string) (AssetRow, error) {
	asset := query.Use(s.db).AssetRow
	row, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(id)).
		Where(ownerConditions(ctx, asset.TableName())...).
		First()
	if isNoRows(err) {
		return AssetRow{}, notFound("资产")
	}
	if err != nil {
		return AssetRow{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) AssetOwnerID(ctx context.Context, id string) (string, error) {
	asset := query.Use(s.db).AssetRow
	row, err := asset.WithContext(ctx).Where(asset.ID.Eq(id)).First()
	if isNoRows(err) {
		return "", notFound("资产")
	}
	if err != nil {
		return "", dbError(err)
	}
	return row.OwnerID, nil
}

func (s *Store) AssetCreate(ctx context.Context, input AssetInput) (AssetRow, error) {
	if strings.TrimSpace(input.Name) == "" {
		return AssetRow{}, badParam(errString("资产名称不能为空"))
	}
	if input.GroupID != nil {
		if _, err := s.groupReference(ctx, s.db, *input.GroupID); err != nil {
			return AssetRow{}, err
		}
	}
	id := ids.New()
	now := ids.NowMS()
	row := AssetRow{
		ID: id, GroupID: input.GroupID, Kind: input.Kind, Name: strings.TrimSpace(input.Name), Host: input.Host, Port: input.Port,
		Username: input.Username, AuthKind: input.AuthKind, KeyPath: input.KeyPath, CredID: input.CredID,
		OptionsJSON: input.OptionsJSON, Tags: input.Tags, Note: input.Note, Sort: input.Sort, CreatedAt: now, UpdatedAt: now, OwnerID: ipc.OwnerID(ctx),
	}
	if err := query.Use(s.db).AssetRow.WithContext(ctx).Create(&row); err != nil {
		return AssetRow{}, dbError(err)
	}
	return s.AssetGet(ctx, id)
}

func (s *Store) AssetUpdate(ctx context.Context, id string, patch AssetPatch) (AssetRow, error) {
	if _, err := s.AssetGet(ctx, id); err != nil {
		return AssetRow{}, err
	}
	values := map[string]any{}
	if patch.GroupID.Set {
		if patch.GroupID.Value != nil {
			if _, err := s.groupReference(ctx, s.db, *patch.GroupID.Value); err != nil {
				return AssetRow{}, err
			}
		}
		values["group_id"] = patch.GroupID.Value
	}
	if patch.Host.Set {
		values["host"] = patch.Host.Value
	}
	if patch.Port.Set {
		values["port"] = patch.Port.Value
	}
	if patch.Username.Set {
		values["username"] = patch.Username.Value
	}
	if patch.AuthKind.Set {
		values["auth_kind"] = patch.AuthKind.Value
	}
	if patch.KeyPath.Set {
		values["key_path"] = patch.KeyPath.Value
	}
	if patch.CredID.Set {
		values["cred_id"] = patch.CredID.Value
	}
	if patch.Name != nil {
		values["name"] = strings.TrimSpace(*patch.Name)
	}
	if patch.OptionsJSON != nil {
		values["options_json"] = *patch.OptionsJSON
	}
	if patch.Tags != nil {
		values["tags"] = *patch.Tags
	}
	if patch.Note != nil {
		values["note"] = *patch.Note
	}
	if patch.Sort != nil {
		values["sort"] = *patch.Sort
	}
	values["updated_at"] = ids.NowMS()
	asset := query.Use(s.db).AssetRow
	_, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(id)).
		Where(ownerConditions(ctx, asset.TableName())...).
		UpdateColumns(values)
	if err != nil {
		return AssetRow{}, dbError(err)
	}
	return s.AssetGet(ctx, id)
}

func applyOptional[T any](current **T, patch Optional[T]) {
	if patch.Set {
		*current = patch.Value
	}
}

func (s *Store) AssetDelete(ctx context.Context, id string) error {
	row, err := s.AssetGet(ctx, id)
	if err != nil {
		return err
	}
	if row.Builtin {
		return badParam(errString("\"当前设备\"是内置资产，不能删除"))
	}
	asset := query.Use(s.db).AssetRow
	_, err = asset.WithContext(ctx).
		Where(asset.ID.Eq(id)).
		Where(ownerConditions(ctx, asset.TableName())...).
		UpdateColumns(map[string]any{"deleted_at": ids.NowMS()})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) AssetEnsureBuiltinLocal(ctx context.Context) (AssetRow, error) {
	row, err := s.AssetGet(ctx, BuiltinLocalAssetID)
	if err == nil {
		if row.DeletedAt == nil {
			return row, nil
		}
		asset := query.Use(s.db).AssetRow
		if _, err := asset.WithContext(ctx).
			Where(asset.ID.Eq(BuiltinLocalAssetID)).
			Where(ownerConditions(ctx, asset.TableName())...).
			UpdateColumns(map[string]any{"deleted_at": nil, "updated_at": ids.NowMS()}); err != nil {
			return AssetRow{}, dbError(err)
		}
		return s.AssetGet(ctx, BuiltinLocalAssetID)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
		return AssetRow{}, err
	}
	if ipc.OwnerID(ctx) != "" {
		return AssetRow{}, notFound("当前设备不可用")
	}
	now := ids.NowMS()
	row = AssetRow{
		ID: BuiltinLocalAssetID, Kind: "local", Name: BuiltinLocalAssetName, OptionsJSON: "{}", Sort: -1,
		CreatedAt: now, UpdatedAt: now, Builtin: true,
	}
	if err := query.Use(s.db).AssetRow.WithContext(ctx).Create(&row); err != nil {
		return AssetRow{}, dbError(err)
	}
	return s.AssetGet(ctx, BuiltinLocalAssetID)
}

func (s *Store) AssetSearch(ctx context.Context, text string) ([]AssetRow, error) {
	like := "%" + strings.ToLower(text) + "%"
	asset := query.Use(s.db).AssetRow
	matches := field.Or(
		asset.Name.Lower().Like(like),
		field.NewUnsafeFieldRaw("lower(coalesce(host,'')) LIKE ?", like),
		field.NewUnsafeFieldRaw("lower(coalesce(username,'')) LIKE ?", like),
		asset.Note.Lower().Like(like),
		asset.Tags.Lower().Like(like),
	)
	result := []AssetRow{}
	err := asset.WithContext(ctx).
		Where(asset.DeletedAt.IsNull(), matches).
		Where(ownerConditions(ctx, asset.TableName())...).
		Order(asset.Sort, asset.Name).
		Limit(100).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) AssetUpsert(ctx context.Context, row AssetRow) (bool, error) {
	return s.assetUpsert(ctx, s.db, row)
}

func (s *Store) AssetUpsertTx(ctx context.Context, tx *gorm.DB, row AssetRow) (bool, error) {
	return s.assetUpsert(ctx, tx, row)
}

func (s *Store) assetUpsert(ctx context.Context, db *gorm.DB, row AssetRow) (bool, error) {
	if row.ID == BuiltinLocalAssetID {
		return false, badParam(errString("内置资产\"当前设备\"不接受同步写入（各设备身份相互独立）"))
	}
	if err := EnsureID(row.ID); err != nil {
		return false, err
	}
	if strings.TrimSpace(row.Name) == "" {
		return false, badParam(errString("资产名称不能为空"))
	}
	if row.GroupID != nil {
		if _, err := s.groupReference(ctx, db, *row.GroupID); err != nil {
			return false, err
		}
	}
	asset := query.Use(db).AssetRow
	_, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(row.ID)).
		Where(ownerConditions(ctx, asset.TableName())...).
		First()
	exists := err == nil
	if err != nil && !isNoRows(err) {
		return false, dbError(err)
	}
	row.OwnerID = ipc.OwnerID(ctx)
	row.Name = strings.TrimSpace(row.Name)
	row.Builtin = false
	if err := s.upsertWithDB(ctx, db, &row, []string{
		"group_id", "kind", "name", "host", "port", "username", "auth_kind", "key_path", "cred_id",
		"options_json", "tags", "note", "sort", "updated_at", "deleted_at",
	}); err != nil {
		return false, err
	}
	return !exists, nil
}
