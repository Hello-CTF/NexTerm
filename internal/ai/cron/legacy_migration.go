package cron

import (
	"context"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ownerMigrationVersion = "conversation-owner-v1"

type OwnerlessMigrationReport struct {
	CronJobsAssigned    int64
	CronJobsQuarantined int64
	RunsAssigned        int64
	MessagesAssigned    int64
	RunEventsAssigned   int64
	HitlRunsAssigned    int64
	CheckpointsAssigned int64
	Unattributed        int64
}

func (r OwnerlessMigrationReport) Changed() bool {
	return r.CronJobsAssigned+r.CronJobsQuarantined+r.RunsAssigned+r.MessagesAssigned+
		r.RunEventsAssigned+r.HitlRunsAssigned+r.CheckpointsAssigned > 0
}

type legacyCronCandidate struct {
	ID        string
	SessionID string
	OwnerKind string
}

type legacyConversationChild struct {
	ID             string
	ConversationID string
}

// MigrateOwnerlessAI attributes only records with a durable parent owner. In
// shared desktop/Open mode, unattributed cron jobs retain their existing behavior.
func MigrateOwnerlessAI(ctx context.Context, database *gorm.DB, shared bool) (OwnerlessMigrationReport, error) {
	var report OwnerlessMigrationReport
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tombstones, err := legacyTombstoneIDs(tx)
		if err != nil {
			return err
		}
		conversationOwners, err := legacyConversationOwners(tx, tombstones)
		if err != nil {
			return err
		}
		if err := migrateLegacyCronJobs(tx, conversationOwners, tombstones, shared, &report); err != nil {
			return err
		}
		if err := migrateLegacyConversationChildren(tx, &store.RunRow{}, "ai_run", conversationOwners, tombstones, &report.RunsAssigned); err != nil {
			return err
		}
		if err := migrateLegacyConversationChildren(tx, &store.MessageRow{}, "ai_message", conversationOwners, tombstones, &report.MessagesAssigned); err != nil {
			return err
		}
		runOwners, err := legacyRunOwners(tx, tombstones)
		if err != nil {
			return err
		}
		if err := migrateLegacyRunEvents(tx, runOwners, tombstones, &report.RunEventsAssigned); err != nil {
			return err
		}
		if err := migrateLegacyRunIDChildren(tx, &store.HitlRunRow{}, "ai_hitl_run", runOwners, tombstones, &report.HitlRunsAssigned); err != nil {
			return err
		}
		if err := migrateLegacyRunIDChildren(tx, &store.CheckpointRow{}, "ai_checkpoint", runOwners, tombstones, &report.CheckpointsAssigned); err != nil {
			return err
		}
		return countUnattributedAIRecords(tx, &report.Unattributed)
	})
	if err != nil {
		return OwnerlessMigrationReport{}, fmt.Errorf("ownerless AI ownership migration: %w", err)
	}
	return report, nil
}

func legacyTombstoneIDs(tx *gorm.DB) (map[string]bool, error) {
	var rows []store.SyncTombstone
	if err := tx.Model(&store.SyncTombstone{}).Select("id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[row.ID] = true
	}
	return ids, nil
}

func legacyConversationOwners(tx *gorm.DB, tombstones map[string]bool) (map[string]string, error) {
	var rows []store.ConversationRow
	if err := tx.Model(&store.ConversationRow{}).Select("id", "owner_id").Where("owner_id <> ''").Scan(&rows).Error; err != nil {
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

func legacyRunOwners(tx *gorm.DB, tombstones map[string]bool) (map[string]string, error) {
	var rows []store.RunRow
	if err := tx.Model(&store.RunRow{}).Select("id", "conversation_id", "owner_id").Where("owner_id <> ''").Scan(&rows).Error; err != nil {
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

func migrateLegacyCronJobs(tx *gorm.DB, conversationOwners map[string]string, tombstones map[string]bool, shared bool, report *OwnerlessMigrationReport) error {
	var jobs []legacyCronCandidate
	if err := tx.Model(&store.CronJob{}).Select("id", "session_id", "owner_kind").Where("owner_id = ''").Scan(&jobs).Error; err != nil {
		return err
	}
	for _, job := range jobs {
		ownerID, attributable := conversationOwners[job.SessionID]
		if tombstones[job.ID] {
			attributable = false
		}
		if attributable && (!shared || job.OwnerKind != string(OwnerKindOpen)) {
			changed, err := assignLegacyOwner(tx, &store.CronJob{}, "cron_job", job.ID, ownerID, map[string]any{
				"owner_id": ownerID, "owner_kind": string(OwnerKindAccount),
			})
			if err != nil {
				return err
			}
			if changed {
				report.CronJobsAssigned++
			}
			continue
		}
		if shared {
			continue
		}
		changed, err := quarantineLegacyCronJob(tx, job.ID)
		if err != nil {
			return err
		}
		if changed {
			report.CronJobsQuarantined++
		}
	}
	return nil
}

func migrateLegacyConversationChildren(tx *gorm.DB, model any, kind string, conversationOwners map[string]string, tombstones map[string]bool, count *int64) error {
	var rows []legacyConversationChild
	if err := tx.Model(model).Select("id", "conversation_id").Where("owner_id = ''").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		ownerID, ok := conversationOwners[row.ConversationID]
		if !ok || tombstones[row.ID] {
			continue
		}
		changed, err := assignLegacyOwner(tx, model, kind, row.ID, ownerID, map[string]any{"owner_id": ownerID})
		if err != nil {
			return err
		}
		if changed {
			(*count)++
		}
	}
	return nil
}

func migrateLegacyRunEvents(tx *gorm.DB, runOwners map[string]string, tombstones map[string]bool, count *int64) error {
	var rows []store.RunEventRow
	if err := tx.Model(&store.RunEventRow{}).Select("run_id", "seq").Where("owner_id = ''").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		recordID := fmt.Sprintf("%s:%d", row.RunID, row.Seq)
		ownerID, ok := runOwners[row.RunID]
		if !ok || tombstones[row.RunID] || tombstones[recordID] {
			continue
		}
		result := tx.Model(&store.RunEventRow{}).
			Where("run_id = ? AND seq = ? AND owner_id = ''", row.RunID, row.Seq).
			UpdateColumns(map[string]any{"owner_id": ownerID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		if err := recordOwnerMigration(tx, "ai_run_event", recordID, ownerID); err != nil {
			return err
		}
		(*count)++
	}
	return nil
}

func migrateLegacyRunIDChildren(tx *gorm.DB, model any, kind string, runOwners map[string]string, tombstones map[string]bool, count *int64) error {
	var rows []struct{ ID string }
	if err := tx.Model(model).Select("id").Where("owner_id = ''").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		ownerID, ok := runOwners[row.ID]
		if !ok || tombstones[row.ID] {
			continue
		}
		changed, err := assignLegacyOwner(tx, model, kind, row.ID, ownerID, map[string]any{"owner_id": ownerID})
		if err != nil {
			return err
		}
		if changed {
			(*count)++
		}
	}
	return nil
}

func assignLegacyOwner(tx *gorm.DB, model any, kind, id, ownerID string, assignments map[string]any) (bool, error) {
	result := tx.Model(model).Where("id = ? AND owner_id = ''", id).UpdateColumns(assignments)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	if err := recordOwnerMigration(tx, kind, id, ownerID); err != nil {
		return false, err
	}
	return true, nil
}

func quarantineLegacyCronJob(tx *gorm.DB, id string) (bool, error) {
	result := tx.Model(&store.CronJob{}).
		Where("id = ? AND owner_id = '' AND (enabled = ? OR owner_kind <> ? OR last_error = '')", id, true, string(OwnerKindLegacy)).
		UpdateColumns(map[string]any{
			"enabled": false, "owner_kind": string(OwnerKindLegacy),
			"last_error": gorm.Expr("CASE WHEN last_error = '' THEN ? ELSE last_error END", ErrOwnerlessLegacy.Error()),
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	if err := recordOwnerMigration(tx, "cron_job_quarantine", id, ""); err != nil {
		return false, err
	}
	return true, nil
}

func recordOwnerMigration(tx *gorm.DB, kind, id, ownerID string) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&store.AIOwnerMigration{
		Migration: ownerMigrationVersion, Kind: kind, RecordID: id, OwnerID: ownerID, AddedAt: ids.NowMS(),
	}).Error
}

func countUnattributedAIRecords(tx *gorm.DB, total *int64) error {
	for _, model := range []any{
		&store.ConversationRow{}, &store.CronJob{}, &store.RunRow{}, &store.MessageRow{},
		&store.RunEventRow{}, &store.HitlRunRow{}, &store.CheckpointRow{},
	} {
		var count int64
		if err := tx.Model(model).Where("owner_id = ''").Count(&count).Error; err != nil {
			return err
		}
		*total += count
	}
	return nil
}
