package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const (
	maxDeviceNameLength = 128
	maxDeviceKindLength = 32
)

type Device struct {
	ID         string
	UserID     string
	Name       string
	Kind       string
	CreatedAt  int64
	LastSeenAt int64
	RevokedAt  int64
}

func (a *Accounts) RegisterDevice(ctx context.Context, userID, name, kind string) (*Device, error) {
	if name == "" || len(name) > maxDeviceNameLength {
		return nil, ipc.BadParam(fmt.Errorf("设备名长度需在 1-%d 之间", maxDeviceNameLength))
	}
	if kind == "" || len(kind) > maxDeviceKindLength {
		return nil, ipc.BadParam(fmt.Errorf("设备类型长度需在 1-%d 之间", maxDeviceKindLength))
	}
	if _, err := a.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	device := &Device{
		ID:        ids.New(),
		UserID:    userID,
		Name:      name,
		Kind:      kind,
		CreatedAt: a.now(),
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO user_device(id, user_id, name, kind, created_at)
VALUES(?,?,?,?,?)`, device.ID, device.UserID, device.Name, device.Kind, device.CreatedAt); err != nil {
		return nil, dbError(err)
	}
	return device, nil
}

func (a *Accounts) GetDevice(ctx context.Context, deviceID string) (*Device, error) {
	row := a.db.QueryRowContext(ctx, "SELECT "+deviceColumns+" FROM user_device WHERE id = ?", deviceID)
	return scanDevice(row)
}

func (a *Accounts) ListDevices(ctx context.Context, userID string) ([]*Device, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT "+deviceColumns+" FROM user_device WHERE user_id = ? ORDER BY created_at, id", userID)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var devices []*Device
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (a *Accounts) TouchDevice(ctx context.Context, deviceID string) error {
	if _, err := a.db.ExecContext(ctx, "UPDATE user_device SET last_seen_at = ? WHERE id = ? AND revoked_at IS NULL", a.now(), deviceID); err != nil {
		return dbError(err)
	}
	return nil
}

func (a *Accounts) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var owner string
	var revokedAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT user_id, revoked_at FROM user_device WHERE id = ?", deviceID).Scan(&owner, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ipc.NewError(ipc.CodeNotFound, "未找到: 设备")
		}
		return dbError(err)
	}
	if owner != userID {
		return ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
	}
	if revokedAt.Valid {
		return ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	}
	now := a.now()
	result, err := tx.ExecContext(ctx, "UPDATE user_device SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now, deviceID)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE user_session SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL", now, deviceID); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sync_credential SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL", now, deviceID); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

const deviceColumns = "id, user_id, name, kind, created_at, last_seen_at, revoked_at"

type rowScanner interface {
	Scan(...any) error
}

func scanDevice(row rowScanner) (*Device, error) {
	var device Device
	var lastSeenAt, revokedAt sql.NullInt64
	if err := row.Scan(&device.ID, &device.UserID, &device.Name, &device.Kind, &device.CreatedAt, &lastSeenAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ipc.NewError(ipc.CodeNotFound, "未找到: 设备")
		}
		return nil, dbError(err)
	}
	if lastSeenAt.Valid {
		device.LastSeenAt = lastSeenAt.Int64
	}
	if revokedAt.Valid {
		device.RevokedAt = revokedAt.Int64
	}
	return &device, nil
}
