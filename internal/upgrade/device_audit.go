package upgrade

import (
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

// backfillDeviceAuditOwners gives fleet audit rows to the account that owns the device named by asset_id.
// Device presence and failover audits were written without an owner, which hides them from the device owner,
// and earlier migrations then handed those ownerless rows to the first superadmin. Ownerless rows of any kind
// are filled in; presence rows (online, offline, failover) are system events whose owner always is the device
// owner, so a different owner is corrected. Other kinds with an owner may be user actions and stay untouched.
// Rows whose device is unknown stay as they are.
func backfillDeviceAuditOwners(database *gorm.DB) (int64, error) {
	const presence = "(kind IN ('device_online','device_offline','device_failover'))"
	var assetIDs []string
	if err := database.Model(&store.AuditRow{}).
		Where("source = ? AND asset_id IS NOT NULL AND (owner_id = '' OR "+presence+")", "fleet").
		Distinct().Pluck("asset_id", &assetIDs).Error; err != nil {
		return 0, err
	}
	var total int64
	for _, assetID := range assetIDs {
		var device store.UserDevice
		result := database.Select("user_id").Where("id = ? AND user_id <> ''", assetID).Limit(1).Find(&device)
		if result.Error != nil {
			return total, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		update := database.Model(&store.AuditRow{}).
			Where("source = ? AND asset_id = ? AND owner_id <> ? AND (owner_id = '' OR "+presence+")",
				"fleet", assetID, device.UserID).
			UpdateColumn("owner_id", device.UserID)
		if update.Error != nil {
			return total, update.Error
		}
		total += update.RowsAffected
	}
	return total, nil
}
