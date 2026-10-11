package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const CipherAES256GCM = "aes256gcm-v1"

const credentialColumns = "id, name, kind, cipher, nonce, blob, kek_hint, created_at, updated_at"

func scanCredential(row rowScanner) (CredentialRow, error) {
	var result CredentialRow
	err := row.Scan(&result.ID, &result.Name, &result.Kind, &result.Cipher, &result.Nonce,
		&result.Blob, &result.KEKHint, &result.CreatedAt, &result.UpdatedAt)
	return result, err
}

func (s *Store) CredentialPut(ctx context.Context, input CredentialInput) (string, error) {
	id := input.ID
	if id == "" {
		id = ids.New()
	}
	now := ids.NowMS()
	_, err := s.db.ExecContext(ctx, `INSERT INTO credential(owner_id, id, name, kind, cipher, nonce, blob, kek_hint, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, cipher=excluded.cipher,
nonce=excluded.nonce, blob=excluded.blob, kek_hint=excluded.kek_hint, updated_at=excluded.updated_at
	WHERE owner_id = ?`,
		ipc.OwnerID(ctx), id, input.Name, input.Kind, CipherAES256GCM, input.Nonce, input.Blob, input.KEKHint, now, now, ipc.OwnerID(ctx))
	if err != nil {
		return "", dbError(err)
	}
	return id, nil
}

func (s *Store) CredentialGetRow(ctx context.Context, id string) (CredentialRow, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	row, err := scanCredential(s.db.QueryRowContext(ctx, "SELECT "+credentialColumns+" FROM credential WHERE id = ?"+condition, append([]any{id}, args...)...))
	if isNoRows(err) {
		return CredentialRow{}, notFound("凭据")
	}
	if err != nil {
		return CredentialRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) CredentialList(ctx context.Context) ([]CredentialRow, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	rows, err := s.db.QueryContext(ctx, "SELECT "+credentialColumns+" FROM credential WHERE 1=1"+condition+" ORDER BY name", args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []CredentialRow{}
	for rows.Next() {
		row, err := scanCredential(rows)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) CredentialDelete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	if _, err := tx.ExecContext(ctx, "DELETE FROM credential WHERE id = ?"+condition, append([]any{id}, args...)...); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credential_tombstone(owner_id, id, deleted_at) VALUES(?,?,?)
ON CONFLICT(owner_id, id) DO UPDATE SET deleted_at=excluded.deleted_at
	WHERE owner_id = ?`, ipc.OwnerID(ctx), id, ids.NowMS(), ipc.OwnerID(ctx)); err != nil {
		return dbError(err)
	}
	return tx.Commit()
}

// CredentialDeleteRow 只删除凭据行本身，不记录删除墓碑；同步协议应用远端墓碑时使用，
// 墓碑修订由调用方经 CredentialTombstonePut 按远端修订单调写入。
func (s *Store) CredentialDeleteRow(ctx context.Context, id string) error {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	_, err := s.db.ExecContext(ctx, "DELETE FROM credential WHERE id = ?"+condition, append([]any{id}, args...)...)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) CredentialUsage(ctx context.Context, credID string) ([]AssetRef, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind FROM asset
WHERE cred_id = ? AND deleted_at IS NULL`+condition+` ORDER BY sort, name`, append([]any{credID}, args...)...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []AssetRef{}
	for rows.Next() {
		var ref AssetRef
		if err := rows.Scan(&ref.ID, &ref.Name, &ref.Kind); err != nil {
			return nil, dbError(err)
		}
		result = append(result, ref)
	}
	return result, rows.Err()
}
