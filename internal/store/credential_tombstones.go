package store

import (
	"context"
)

type CredentialTombstone struct {
	ID        string `json:"id"`
	DeletedAt int64  `json:"deletedAt"`
}

func (s *Store) CredentialTombstoneList(ctx context.Context) ([]CredentialTombstone, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, deleted_at FROM credential_tombstone ORDER BY id")
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
	err := s.db.QueryRowContext(ctx, "SELECT id, deleted_at FROM credential_tombstone WHERE id = ?", id).
		Scan(&tombstone.ID, &tombstone.DeletedAt)
	if isNoRows(err) {
		return CredentialTombstone{}, notFound("凭据墓碑 " + id)
	}
	if err != nil {
		return CredentialTombstone{}, dbError(err)
	}
	return tombstone, nil
}

func (s *Store) CredentialTombstonePut(ctx context.Context, id string, deletedAt int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO credential_tombstone(id, deleted_at) VALUES(?,?)
ON CONFLICT(id) DO UPDATE SET deleted_at=`+s.dialect.ScalarMax()+`(deleted_at, excluded.deleted_at)`, id, deletedAt)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) CredentialTombstoneClear(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM credential_tombstone WHERE id = ?", id)
	if err != nil {
		return dbError(err)
	}
	return nil
}
