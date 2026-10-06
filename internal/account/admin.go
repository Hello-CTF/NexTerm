package account

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const registrationSettingKey = "auth.registration_open"

func (a *Accounts) CountUsers(ctx context.Context) (int, error) {
	var count int
	if err := a.db.QueryRowContext(ctx, "SELECT count(*) FROM "+userTable).Scan(&count); err != nil {
		return 0, dbError(err)
	}
	return count, nil
}

func (a *Accounts) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT "+userColumns+" FROM "+userTable+" ORDER BY created_at, id")
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var users []*User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// SetUserDisabled 禁用或恢复账号; 禁用立即吊销其全部会话。
func (a *Accounts) SetUserDisabled(ctx context.Context, userID string, disabled bool) error {
	target := StateActive
	if disabled {
		target = StateDisabled
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, "UPDATE "+userTable+" SET state = ?, updated_at = ? WHERE id = ?",
		string(target), a.now(), userID)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "未找到: 用户")
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, "UPDATE user_session SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL", a.now(), userID); err != nil {
			return dbError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

// RegistrationEnabled 读取开放注册开关; 缺省关闭。
func (a *Accounts) RegistrationEnabled(ctx context.Context) (bool, error) {
	var value string
	err := a.db.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", registrationSettingKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	return value == "true", nil
}

func (a *Accounts) SetRegistrationEnabled(ctx context.Context, enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		registrationSettingKey, value, a.now()); err != nil {
		return dbError(err)
	}
	return nil
}
