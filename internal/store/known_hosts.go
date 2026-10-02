package store

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func scanKnownHost(row rowScanner) (KnownHostRow, error) {
	var result KnownHostRow
	err := row.Scan(&result.ID, &result.Host, &result.Port, &result.KeyType, &result.Fingerprint, &result.AddedAt)
	return result, err
}

func (s *Store) KnownHostGet(ctx context.Context, host string, port int32, keyType string) (KnownHostRow, bool, error) {
	row, err := scanKnownHost(s.db.QueryRowContext(ctx,
		"SELECT id, host, port, key_type, fingerprint, added_at FROM known_host WHERE host=? AND port=? AND key_type=?",
		host, port, keyType))
	if isNoRows(err) {
		return KnownHostRow{}, false, nil
	}
	if err != nil {
		return KnownHostRow{}, false, dbError(err)
	}
	return row, true, nil
}

func (s *Store) KnownHostAccept(ctx context.Context, host string, port int32, keyType, fingerprint string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO known_host(id, host, port, key_type, fingerprint, added_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(host, port, key_type) DO UPDATE SET fingerprint=excluded.fingerprint, added_at=excluded.added_at`,
		ids.New(), host, port, keyType, fingerprint, ids.NowMS())
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) KnownHostList(ctx context.Context) ([]KnownHostRow, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, host, port, key_type, fingerprint, added_at FROM known_host ORDER BY host, port")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []KnownHostRow{}
	for rows.Next() {
		row, err := scanKnownHost(rows)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) KnownHostRemove(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM known_host WHERE id = ?", id)
	if err != nil {
		return dbError(err)
	}
	return nil
}
