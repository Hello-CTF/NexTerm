package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// localDeviceSettingKey holds this database's device ID and the install
// fingerprint it was issued under.
const localDeviceSettingKey = "sync.local_device"

// LocalDevice identifies the installation that runs a database's jobs.
type LocalDevice struct {
	ID   string
	Name string
}

type localDeviceRecord struct {
	ID      string `json:"id"`
	Install string `json:"install,omitempty"`
}

// localDeviceAttempts bounds the retries when another opener replaced the
// record concurrently.
const localDeviceAttempts = 8

// LoadLocalDevice returns the database's device ID, creating it on first use,
// and this host's name. install is a fingerprint kept outside the database by
// the installation: a database copied or restored under another installation
// carries a foreign fingerprint, so it gets a new device ID and the jobs bound
// to the old one become foreign there. An empty install skips that check.
// Concurrent openers agree on one ID.
func LoadLocalDevice(ctx context.Context, db *gorm.DB, install string) (LocalDevice, error) {
	name, _ := os.Hostname()
	name = strings.TrimSpace(name)
	setting := query.Use(db).Setting
	for attempt := 0; attempt < localDeviceAttempts; attempt++ {
		fresh, err := json.Marshal(localDeviceRecord{ID: ids.New(), Install: install})
		if err != nil {
			return LocalDevice{}, err
		}
		if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Create(&Setting{Key: localDeviceSettingKey, Value: string(fresh), UpdatedAt: ids.NowMS()}).Error; err != nil {
			return LocalDevice{}, dbError(fmt.Errorf("create the local device ID: %w", err))
		}
		row, err := setting.WithContext(ctx).Where(setting.Key.Eq(localDeviceSettingKey)).First()
		if err != nil {
			return LocalDevice{}, dbError(fmt.Errorf("read the local device ID: %w", err))
		}
		var record localDeviceRecord
		if json.Unmarshal([]byte(row.Value), &record) == nil && record.ID != "" && (install == "" || record.Install == install) {
			return LocalDevice{ID: record.ID, Name: name}, nil
		}
		// Issued under another installation: take a new ID unless another
		// opener replaced the record first, then read it again.
		if _, err := setting.WithContext(ctx).
			Where(setting.Key.Eq(localDeviceSettingKey), setting.Value.Eq(row.Value)).
			UpdateColumns(map[string]any{"value": string(fresh), "updated_at": ids.NowMS()}); err != nil {
			return LocalDevice{}, dbError(fmt.Errorf("replace the local device ID: %w", err))
		}
	}
	return LocalDevice{}, dbError(fmt.Errorf("the local device ID kept changing"))
}
