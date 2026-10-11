package account

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const registrationSettingKey = "auth.registration_open"

func (a *Accounts) CountUsers(ctx context.Context) (int, error) {
	appUser := query.Use(a.db).AppUser
	count, err := appUser.WithContext(ctx).Count()
	if err != nil {
		return 0, dbError(err)
	}
	return int(count), nil
}

func (a *Accounts) ListUsers(ctx context.Context) ([]*User, error) {
	appUser := query.Use(a.db).AppUser
	rows := []store.AppUser{}
	if err := appUser.WithContext(ctx).Order(appUser.CreatedAt, appUser.ID).Scan(&rows); err != nil {
		return nil, dbError(err)
	}
	users := make([]*User, 0, len(rows))
	for _, row := range rows {
		users = append(users, userFromModel(&row))
	}
	return users, nil
}

func (a *Accounts) SetUserDisabled(ctx context.Context, userID string, disabled bool) error {
	target := StateActive
	if disabled {
		target = StateDisabled
	}
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		appUser := query.Use(tx).AppUser
		info, err := appUser.WithContext(ctx).
			Where(appUser.ID.Eq(userID)).
			UpdateColumns(map[string]any{"state": string(target), "updated_at": a.now()})
		if err != nil {
			return dbError(err)
		}
		if info.RowsAffected == 0 {
			return ipc.NewError(ipc.CodeNotFound, "用户不存在")
		}
		if disabled {
			userSession := query.Use(tx).UserSession
			if _, err := userSession.WithContext(ctx).
				Where(userSession.UserID.Eq(userID), userSession.RevokedAt.IsNull()).
				UpdateColumns(map[string]any{"revoked_at": a.now()}); err != nil {
				return dbError(err)
			}
		}
		return nil
	})
}

func (a *Accounts) RegistrationEnabled(ctx context.Context) (bool, error) {
	setting := query.Use(a.db).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(registrationSettingKey)).First()
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, dbError(err)
	}
	return row.Value == "true", nil
}

func (a *Accounts) SetRegistrationEnabled(ctx context.Context, enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	if err := a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&store.Setting{Key: registrationSettingKey, Value: value, UpdatedAt: a.now()}).Error; err != nil {
		return dbError(err)
	}
	return nil
}
