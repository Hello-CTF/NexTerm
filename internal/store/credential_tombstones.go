package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type CredentialTombstone struct {
	ID        string `json:"id"`
	DeletedAt int64  `json:"deletedAt"`
}

func (s *Store) CredentialTombstoneList(ctx context.Context) ([]CredentialTombstone, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	rows, err := s.db.QueryContext(ctx, "SELECT id, deleted_at FROM credential_tombstone WHERE 1=1"+condition+" ORDER BY id", args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []CredentialTombstone{}
	for rows.Next() {
		var tombstone CredentialTombstone
		if err := rows.Scan(&tombstone.ID, &tombstone.DeletedAt); err != nil {
			return nil, dbError(err)
		}
		result = append(result, tombstone)
	}
	return result, rows.Err()
}

func (s *Store) CredentialTombstoneGet(ctx context.Context, id string) (CredentialTombstone, error) {
	var tombstone CredentialTombstone
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	err := s.db.QueryRowContext(ctx, "SELECT id, deleted_at FROM credential_tombstone WHERE id = ?"+condition, append([]any{id}, args...)...).Scan(&tombstone.ID, &tombstone.DeletedAt)
	if isNoRows(err) {
		return CredentialTombstone{}, notFound("凭据墓碑")
	}
	if err != nil {
		return CredentialTombstone{}, dbError(err)
	}
	return tombstone, nil
}

func (s *Store) CredentialTombstonePut(ctx context.Context, id string, deletedAt int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO credential_tombstone(owner_id, id, deleted_at) VALUES(?,?,?)
ON CONFLICT(owner_id, id) DO UPDATE SET deleted_at=`+s.dialect.ScalarMax()+`(credential_tombstone.deleted_at, excluded.deleted_at)
WHERE owner_id = ?`, ipc.OwnerID(ctx), id, deletedAt, ipc.OwnerID(ctx))
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) CredentialTombstoneClear(ctx context.Context, id string) error {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	_, err := s.db.ExecContext(ctx, "DELETE FROM credential_tombstone WHERE id = ?"+condition, append([]any{id}, args...)...)
	if err != nil {
		return dbError(err)
	}
	return nil
}
