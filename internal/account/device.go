package account

import (
	"context"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
	"gorm.io/gorm"
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
	device := &Device{ID: ids.New(), UserID: userID, Name: name, Kind: kind, CreatedAt: a.now()}
	row := store.UserDevice{ID: device.ID, UserID: device.UserID, Name: device.Name, Kind: device.Kind, CreatedAt: device.CreatedAt}
	if err := query.Use(a.db).UserDevice.WithContext(ctx).Create(&row); err != nil {
		return nil, dbError(err)
	}
	return device, nil
}

func (a *Accounts) GetDevice(ctx context.Context, deviceID string) (*Device, error) {
	userDevice := query.Use(a.db).UserDevice
	row, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(deviceID)).First()
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ipc.NewError(ipc.CodeNotFound, "设备不存在")
		}
		return nil, dbError(err)
	}
	return deviceFromModel(row), nil
}

func (a *Accounts) ListDevices(ctx context.Context, userID string) ([]*Device, error) {
	userDevice := query.Use(a.db).UserDevice
	rows := []store.UserDevice{}
	if err := userDevice.WithContext(ctx).
		Where(userDevice.UserID.Eq(userID)).
		Order(userDevice.CreatedAt, userDevice.ID).
		Scan(&rows); err != nil {
		return nil, dbError(err)
	}
	devices := make([]*Device, 0, len(rows))
	for _, row := range rows {
		devices = append(devices, deviceFromModel(&row))
	}
	return devices, nil
}

func (a *Accounts) TouchDevice(ctx context.Context, deviceID string) error {
	return touchDeviceAt(ctx, a.db, deviceID, a.now())
}

func touchDeviceAt(ctx context.Context, db *gorm.DB, deviceID string, now int64) error {
	userDevice := query.Use(db).UserDevice
	_, err := userDevice.WithContext(ctx).
		Where(userDevice.ID.Eq(deviceID), userDevice.RevokedAt.IsNull()).
		Where(field.Or(userDevice.LastSeenAt.IsNull(), userDevice.LastSeenAt.Lt(now))).
		UpdateColumns(map[string]any{"last_seen_at": now})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (a *Accounts) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	err := a.withBusyRetry(ctx, func() error {
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			userDevice := query.Use(tx).UserDevice
			row, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(deviceID)).First()
			if err == gorm.ErrRecordNotFound {
				return ipc.NewError(ipc.CodeNotFound, "设备不存在")
			}
			if err != nil {
				return dbError(err)
			}
			if row.UserID != userID {
				return ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
			}
			if row.RevokedAt != nil {
				return ipc.NewError(ipc.CodeForbidden, "设备已吊销")
			}
			now := a.now()
			info, err := userDevice.WithContext(ctx).
				Where(userDevice.ID.Eq(deviceID), userDevice.RevokedAt.IsNull()).
				UpdateColumns(map[string]any{"revoked_at": now})
			if err != nil {
				return dbError(err)
			}
			if info.RowsAffected == 0 {
				return ipc.NewError(ipc.CodeForbidden, "设备已吊销")
			}
			userSession := query.Use(tx).UserSession
			if _, err := userSession.WithContext(ctx).
				Where(userSession.DeviceID.Eq(deviceID), userSession.RevokedAt.IsNull()).
				UpdateColumns(map[string]any{"revoked_at": now}); err != nil {
				return dbError(err)
			}
			syncCredential := query.Use(tx).SyncCredential
			if _, err := syncCredential.WithContext(ctx).
				Where(syncCredential.DeviceID.Eq(deviceID), syncCredential.RevokedAt.IsNull()).
				UpdateColumns(map[string]any{"revoked_at": now}); err != nil {
				return dbError(err)
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	a.epochs.bump(userID)
	return nil
}

func deviceFromModel(row *store.UserDevice) *Device {
	device := &Device{ID: row.ID, UserID: row.UserID, Name: row.Name, Kind: row.Kind, CreatedAt: row.CreatedAt}
	if row.LastSeenAt != nil {
		device.LastSeenAt = *row.LastSeenAt
	}
	if row.RevokedAt != nil {
		device.RevokedAt = *row.RevokedAt
	}
	return device
}
