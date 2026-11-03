package upgrade

import (
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

// backfillDeviceAuditOwners gives ownerless fleet audit rows to the account that owns the device named by asset_id.
// Device presence and failover audits were written without an owner, which hides them from the device owner.
// Rows whose device is unknown stay ownerless for the regular ownership migration.
func backfillDeviceAuditOwners(database *gorm.DB) (int64, error) {
	var assetIDs []string
	if err := database.Model(&store.AuditRow{}).
		Where("owner_id = '' AND source = ? AND asset_id IS NOT NULL", "fleet").
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
			Where("owner_id = '' AND source = ? AND asset_id = ?", "fleet", assetID).
			UpdateColumn("owner_id", device.UserID)
		if update.Error != nil {
			return total, update.Error
		}
		total += update.RowsAffected
	}
	return total, nil
}
