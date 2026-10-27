package store

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// localDeviceSettingKey holds this database's random device ID.
const localDeviceSettingKey = "sync.local_device_id"

// LocalDevice identifies the installation that owns a database: jobs bound
// to it run here and nowhere else.
type LocalDevice struct {
	ID   string
	Name string
}

// LoadLocalDevice returns the database's device ID, creating it on first
// use, and this host's name. Concurrent first calls agree on one ID.
func LoadLocalDevice(ctx context.Context, db *gorm.DB) (LocalDevice, error) {
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&Setting{Key: localDeviceSettingKey, Value: ids.New(), UpdatedAt: ids.NowMS()}).Error; err != nil {
		return LocalDevice{}, dbError(fmt.Errorf("create the local device ID: %w", err))
	}
	setting := query.Use(db).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(localDeviceSettingKey)).First()
	if err != nil {
		return LocalDevice{}, dbError(fmt.Errorf("read the local device ID: %w", err))
	}
	name, _ := os.Hostname()
	return LocalDevice{ID: row.Value, Name: strings.TrimSpace(name)}, nil
}
