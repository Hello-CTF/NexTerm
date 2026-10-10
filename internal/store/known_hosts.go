package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
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

// KnownHostRemove 删除已知主机; 仅当删除者的 known_host 同步 opt-in 开启时在同一事务记录用户删除墓碑,
// 让删除随下一轮同步收敛。opt-in 按账号用户隔离且默认开启: 未开启(或拿不到删除者身份, 如桌面直连/匿名模式)时
// 只删本地行, 从未同步或已禁用同步的主机删除不得产生墓碑, 远端副本不受影响。
func (s *Store) KnownHostRemove(ctx context.Context, id string) error {
	userID, hasIdentity := SyncOptInUserID(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	optIn := hasIdentity
	if hasIdentity {
		var optInRaw string
		optInErr := tx.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", knownHostOptInKey(userID)).Scan(&optInRaw)
		if optInErr != nil && !isNoRows(optInErr) {
			return dbError(optInErr)
		}
		if optInErr == nil {
			optIn = optInRaw == "1"
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM known_host WHERE id = ?", id); err != nil {
		return dbError(err)
	}
	if optIn {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_tombstone(id, kind, deleted_at) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET deleted_at = `+s.dialect.ScalarMax()+`(sync_tombstone.deleted_at, excluded.deleted_at)`,
			id, SyncTombstoneKindKnownHost, ids.NowMS()); err != nil {
			return dbError(err)
		}
	}
	return tx.Commit()
}
