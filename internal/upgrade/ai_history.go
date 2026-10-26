package upgrade

import (
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

// AIHistoryReport counts AI history created in Open mode (empty owner_id) that
// was attributed to the owner of its conversation after switching to account
// mode. Unattributed counts AI rows that remain ownerless afterwards.
type AIHistoryReport struct {
	CronJobsAssigned    int64
	CronJobsDisabled    int64
	RunsAssigned        int64
	MessagesAssigned    int64
	RunEventsAssigned   int64
	HitlRunsAssigned    int64
	CheckpointsAssigned int64
	Unattributed        int64
}

func (r AIHistoryReport) Changed() bool {
	return r.CronJobsAssigned+r.CronJobsDisabled+r.RunsAssigned+r.MessagesAssigned+
		r.RunEventsAssigned+r.HitlRunsAssigned+r.CheckpointsAssigned > 0
}

var aiHistoryChildModels = []any{
	&store.RunRow{},
	&store.MessageRow{},
	&store.RunEventRow{},
	&store.HitlRunRow{},
	&store.CheckpointRow{},
}

func aiHistoryMigrationPending(database *gorm.DB) (bool, error) {
	for _, model := range aiHistoryChildModels {
		found, err := hasOwnerlessRows(database.Model(model))
		if err != nil || found {
			return found, err
		}
	}
	return hasOwnerlessRows(database.Model(&store.CronJob{}).
		Where("owner_kind = ? AND enabled = ?", string(cron.OwnerKindOpen), true))
}

// migrateAIHistory runs after conversations received their owner. Child rows
// follow their conversation (or run) owner; Open cron jobs become account jobs
// of the conversation owner, and Open cron jobs without an attributable
// conversation are disabled with a visible error instead of failing each run.
func migrateAIHistory(database *gorm.DB, report *AIHistoryReport) error {
	tombstones, err := aiTombstonedIDs(database)
	if err != nil {
		return err
	}
	conversationOwners, err := aiConversationOwners(database, tombstones)
	if err != nil {
		return err
	}
	if err := migrateOpenCronJobs(database, conversationOwners, tombstones, report); err != nil {
		return err
	}
	if err := assignConversationChildren(database, &store.RunRow{}, conversationOwners, tombstones, &report.RunsAssigned); err != nil {
		return err
	}
	if err := assignConversationChildren(database, &store.MessageRow{}, conversationOwners, tombstones, &report.MessagesAssigned); err != nil {
		return err
	}
	runOwners, err := aiRunOwners(database, tombstones)
	if err != nil {
		return err
	}
	if err := assignRunEvents(database, runOwners, tombstones, &report.RunEventsAssigned); err != nil {
		return err
	}
	if err := assignRunChildren(database, &store.HitlRunRow{}, runOwners, tombstones, &report.HitlRunsAssigned); err != nil {
		return err
	}
	if err := assignRunChildren(database, &store.CheckpointRow{}, runOwners, tombstones, &report.CheckpointsAssigned); err != nil {
		return err
	}
	return countUnattributedAI(database, &report.Unattributed)
}

func aiTombstonedIDs(database *gorm.DB) (map[string]bool, error) {
	var ids []string
	if err := database.Model(&store.SyncTombstone{}).Distinct("id").Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(ids))
	for _, id := range ids {
		result[id] = true
	}
	return result, nil
}

func aiConversationOwners(database *gorm.DB, tombstones map[string]bool) (map[string]string, error) {
	var rows []store.ConversationRow
	if err := database.Model(&store.ConversationRow{}).Select("id", "owner_id").Where("owner_id <> ''").Scan(&rows).Error; err != nil {
		return nil, err
	}
	owners := make(map[string]string, len(rows))
	for _, row := range rows {
		if !tombstones[row.ID] {
			owners[row.ID] = row.OwnerID
		}
	}
	return owners, nil
}

func aiRunOwners(database *gorm.DB, tombstones map[string]bool) (map[string]string, error) {
	var rows []store.RunRow
	if err := database.Model(&store.RunRow{}).Select("id", "conversation_id", "owner_id").Where("owner_id <> ''").Scan(&rows).Error; err != nil {
		return nil, err
	}
	owners := make(map[string]string, len(rows))
	for _, row := range rows {
		if !tombstones[row.ID] && !tombstones[row.ConversationID] {
			owners[row.ID] = row.OwnerID
		}
	}
	return owners, nil
}

func migrateOpenCronJobs(database *gorm.DB, conversationOwners map[string]string, tombstones map[string]bool, report *AIHistoryReport) error {
	var jobs []struct {
		ID        string
		SessionID string
	}
	if err := database.Model(&store.CronJob{}).Select("id", "session_id").
		Where("owner_id = '' AND owner_kind = ?", string(cron.OwnerKindOpen)).
		Order("id").Scan(&jobs).Error; err != nil {
		return err
	}
	for _, job := range jobs {
		ownerID, attributable := conversationOwners[job.SessionID]
		if attributable && !tombstones[job.ID] {
			result := database.Model(&store.CronJob{}).
				Where("id = ? AND owner_id = '' AND owner_kind = ?", job.ID, string(cron.OwnerKindOpen)).
				UpdateColumns(map[string]any{"owner_id": ownerID, "owner_kind": string(cron.OwnerKindAccount)})
			if result.Error != nil {
				return result.Error
			}
			report.CronJobsAssigned += result.RowsAffected
			continue
		}
		result := database.Model(&store.CronJob{}).
			Where("id = ? AND owner_id = '' AND owner_kind = ? AND enabled = ?", job.ID, string(cron.OwnerKindOpen), true).
			UpdateColumns(map[string]any{"enabled": false, "last_error": cron.ErrOpenJobUnattributed.Error()})
		if result.Error != nil {
			return result.Error
		}
		report.CronJobsDisabled += result.RowsAffected
	}
	return nil
}

func assignConversationChildren(database *gorm.DB, model any, conversationOwners map[string]string, tombstones map[string]bool, count *int64) error {
	var rows []struct {
		ID             string
		ConversationID string
	}
	if err := database.Model(model).Select("id", "conversation_id").Where("owner_id = ''").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		ownerID, ok := conversationOwners[row.ConversationID]
		if !ok || tombstones[row.ID] {
			continue
		}
		result := database.Model(model).Where("id = ? AND owner_id = ''", row.ID).UpdateColumn("owner_id", ownerID)
		if result.Error != nil {
			return result.Error
		}
		*count += result.RowsAffected
	}
	return nil
}

func assignRunEvents(database *gorm.DB, runOwners map[string]string, tombstones map[string]bool, count *int64) error {
	var runIDs []string
	if err := database.Model(&store.RunEventRow{}).Where("owner_id = ''").Distinct("run_id").Pluck("run_id", &runIDs).Error; err != nil {
		return err
	}
	for _, runID := range runIDs {
		ownerID, ok := runOwners[runID]
		if !ok || tombstones[runID] {
			continue
		}
		result := database.Model(&store.RunEventRow{}).Where("run_id = ? AND owner_id = ''", runID).UpdateColumn("owner_id", ownerID)
		if result.Error != nil {
			return result.Error
		}
		*count += result.RowsAffected
	}
	return nil
}

func assignRunChildren(database *gorm.DB, model any, runOwners map[string]string, tombstones map[string]bool, count *int64) error {
	var runIDs []string
	if err := database.Model(model).Where("owner_id = ''").Pluck("id", &runIDs).Error; err != nil {
		return err
	}
	for _, runID := range runIDs {
		ownerID, ok := runOwners[runID]
		if !ok || tombstones[runID] {
			continue
		}
		result := database.Model(model).Where("id = ? AND owner_id = ''", runID).UpdateColumn("owner_id", ownerID)
		if result.Error != nil {
			return result.Error
		}
		*count += result.RowsAffected
	}
	return nil
}

func countUnattributedAI(database *gorm.DB, total *int64) error {
	for _, model := range append([]any{&store.CronJob{}}, aiHistoryChildModels...) {
		var count int64
		if err := database.Model(model).Where("owner_id = ''").Count(&count).Error; err != nil {
			return err
		}
		*total += count
	}
	return nil
}
