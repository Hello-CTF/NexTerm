package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const assetColumns = `id, group_id, kind, name, host, port, username, auth_kind, key_path,
cred_id, options_json, tags, note, sort, created_at, updated_at, deleted_at, builtin`

func scanAsset(row rowScanner) (AssetRow, error) {
	var result AssetRow
	err := row.Scan(&result.ID, &result.GroupID, &result.Kind, &result.Name, &result.Host,
		&result.Port, &result.Username, &result.AuthKind, &result.KeyPath, &result.CredID,
		&result.OptionsJSON, &result.Tags, &result.Note, &result.Sort, &result.CreatedAt,
		&result.UpdatedAt, &result.DeletedAt, &result.Builtin)
	return result, err
}

func (s *Store) AssetList(ctx context.Context, includeDeleted bool) ([]AssetRow, error) {
	query := "SELECT " + assetColumns + " FROM asset"
	if !includeDeleted {
		query += " WHERE deleted_at IS NULL"
	}
	query += " ORDER BY sort, name"
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []AssetRow{}
	for rows.Next() {
		row, err := scanAsset(rows)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) AssetGet(ctx context.Context, id string) (AssetRow, error) {
	row, err := scanAsset(s.db.QueryRowContext(ctx, "SELECT "+assetColumns+" FROM asset WHERE id = ?", id))
	if isNoRows(err) {
		return AssetRow{}, notFound("资产")
	}
	if err != nil {
		return AssetRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) AssetCreate(ctx context.Context, input AssetInput) (AssetRow, error) {
	if strings.TrimSpace(input.Name) == "" {
		return AssetRow{}, badParam(errString("资产名称不能为空"))
	}
	id := ids.New()
	now := ids.NowMS()
	_, err := s.db.ExecContext(ctx, `INSERT INTO asset(id, group_id, kind, name, host, port, username,
auth_kind, key_path, cred_id, options_json, tags, note, sort, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, input.GroupID, input.Kind, strings.TrimSpace(input.Name),
		input.Host, input.Port, input.Username, input.AuthKind, input.KeyPath, input.CredID,
		input.OptionsJSON, input.Tags, input.Note, input.Sort, now, now)
	if err != nil {
		return AssetRow{}, dbError(err)
	}
	return s.AssetGet(ctx, id)
}

// AssetUpdate 只把 patch 显式给出的列写进 SQL, 并发 patch 不同字段互不覆盖。
func (s *Store) AssetUpdate(ctx context.Context, id string, patch AssetPatch) (AssetRow, error) {
	if _, err := s.AssetGet(ctx, id); err != nil {
		return AssetRow{}, err
	}
	sets := make([]string, 0, 13)
	args := make([]any, 0, 14)
	add := func(column string, value any) {
		sets = append(sets, column+"=?")
		args = append(args, value)
	}
	if patch.GroupID.Set {
		add("group_id", patch.GroupID.Value)
	}
	if patch.Host.Set {
		add("host", patch.Host.Value)
	}
	if patch.Port.Set {
		add("port", patch.Port.Value)
	}
	if patch.Username.Set {
		add("username", patch.Username.Value)
	}
	if patch.AuthKind.Set {
		add("auth_kind", patch.AuthKind.Value)
	}
	if patch.KeyPath.Set {
		add("key_path", patch.KeyPath.Value)
	}
	if patch.CredID.Set {
		add("cred_id", patch.CredID.Value)
	}
	if patch.Name != nil {
		add("name", strings.TrimSpace(*patch.Name))
	}
	if patch.OptionsJSON != nil {
		add("options_json", *patch.OptionsJSON)
	}
	if patch.Tags != nil {
		add("tags", *patch.Tags)
	}
	if patch.Note != nil {
		add("note", *patch.Note)
	}
	if patch.Sort != nil {
		add("sort", *patch.Sort)
	}
	add("updated_at", ids.NowMS())
	args = append(args, id)
	if _, err := s.db.ExecContext(ctx, "UPDATE asset SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
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
	_, err = s.db.ExecContext(ctx, "UPDATE asset SET deleted_at = ? WHERE id = ?", ids.NowMS(), id)
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
		if _, err := s.db.ExecContext(ctx, "UPDATE asset SET deleted_at=NULL, updated_at=? WHERE id=?",
			ids.NowMS(), BuiltinLocalAssetID); err != nil {
			return AssetRow{}, dbError(err)
		}
		return s.AssetGet(ctx, BuiltinLocalAssetID)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
		return AssetRow{}, err
	}
	now := ids.NowMS()
	_, err = s.db.ExecContext(ctx, `INSERT INTO asset(id, group_id, kind, name, host, port, username,
auth_kind, key_path, cred_id, options_json, tags, note, sort, created_at, updated_at, builtin)
VALUES(?,NULL,'local',?,NULL,NULL,NULL,NULL,NULL,NULL,'{}','','',-1,?,?,1)`,
		BuiltinLocalAssetID, BuiltinLocalAssetName, now, now)
	if err != nil {
		return AssetRow{}, dbError(err)
	}
	return s.AssetGet(ctx, BuiltinLocalAssetID)
}

func (s *Store) AssetSearch(ctx context.Context, query string) ([]AssetRow, error) {
	like := "%" + strings.ToLower(query) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT `+assetColumns+` FROM asset
WHERE deleted_at IS NULL AND (lower(name) LIKE ? OR lower(coalesce(host,'')) LIKE ?
OR lower(coalesce(username,'')) LIKE ? OR lower(note) LIKE ? OR lower(tags) LIKE ?)
ORDER BY sort, name LIMIT 100`, like, like, like, like, like)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []AssetRow{}
	for rows.Next() {
		row, err := scanAsset(rows)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type assetExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) AssetUpsert(ctx context.Context, row AssetRow) (bool, error) {
	return assetUpsert(ctx, s.db, row)
}

// AssetUpsertTx 是 AssetUpsert 的事务内版本, 供同步导入把一个对象类的多次写入并入同一事务。
func (s *Store) AssetUpsertTx(ctx context.Context, tx *sql.Tx, row AssetRow) (bool, error) {
	return assetUpsert(ctx, tx, row)
}

func assetUpsert(ctx context.Context, exec assetExecer, row AssetRow) (bool, error) {
	if row.ID == BuiltinLocalAssetID {
		return false, badParam(errString("内置资产\"当前设备\"不接受同步写入（本机在每台设备上都是各自身份）"))
	}
	if err := EnsureID(row.ID); err != nil {
		return false, err
	}
	if strings.TrimSpace(row.Name) == "" {
		return false, badParam(errString("资产名称不能为空"))
	}
	var ignored string
	err := exec.QueryRowContext(ctx, "SELECT id FROM asset WHERE id = ?", row.ID).Scan(&ignored)
	exists := err == nil
	if err != nil && !isNoRows(err) {
		return false, dbError(err)
	}
	_, err = exec.ExecContext(ctx, `INSERT INTO asset(id, group_id, kind, name, host, port, username,
auth_kind, key_path, cred_id, options_json, tags, note, sort, created_at, updated_at, deleted_at, builtin)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)
ON CONFLICT(id) DO UPDATE SET group_id=excluded.group_id, kind=excluded.kind, name=excluded.name,
host=excluded.host, port=excluded.port, username=excluded.username, auth_kind=excluded.auth_kind,
key_path=excluded.key_path, cred_id=excluded.cred_id, options_json=excluded.options_json,
tags=excluded.tags, note=excluded.note, sort=excluded.sort, updated_at=excluded.updated_at,
deleted_at=excluded.deleted_at`, row.ID, row.GroupID, row.Kind, strings.TrimSpace(row.Name), row.Host,
		row.Port, row.Username, row.AuthKind, row.KeyPath, row.CredID, row.OptionsJSON, row.Tags,
		row.Note, row.Sort, row.CreatedAt, row.UpdatedAt, row.DeletedAt)
	if err != nil {
		return false, dbError(err)
	}
	return !exists, nil
}
