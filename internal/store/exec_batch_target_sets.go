package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm"
)

type ExecTargetSetRow = model.ExecTargetSetRow
type ExecTargetSetItemRow = model.ExecTargetSetItemRow

const (
	ExecTargetSetMaxNameBytes    = 128
	ExecTargetSetMaxTargets      = 64
	execTargetSetMaxSessionIDLen = 191
)

type ExecTargetSet struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	SessionIDs []string `json:"sessionIds"`
	CreatedAt  int64    `json:"createdAt"`
	UpdatedAt  int64    `json:"updatedAt"`
}

func (s *Store) ExecTargetSetCreate(ctx context.Context, name string, sessionIDs []string) (ExecTargetSet, error) {
	trimmed, err := validateExecTargetSetName(name)
	if err != nil {
		return ExecTargetSet{}, badParam(err)
	}
	if err := validateExecTargetSetSessions(sessionIDs); err != nil {
		return ExecTargetSet{}, badParam(err)
	}
	ownerID := ipc.OwnerID(ctx)
	id := ids.New()
	now := ids.NowMS()
	row := ExecTargetSetRow{OwnerID: ownerID, ID: id, Name: trimmed, CreatedAt: now, UpdatedAt: now}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return createExecTargetSetItems(ctx, tx, ownerID, id, sessionIDs)
	})
	if err != nil {
		return ExecTargetSet{}, dbError(err)
	}
	return s.ExecTargetSetGet(ctx, id)
}

func (s *Store) ExecTargetSetList(ctx context.Context) ([]ExecTargetSet, error) {
	ownerID := ipc.OwnerID(ctx)
	rows := []ExecTargetSetRow{}
	if err := s.db.WithContext(ctx).
		Where("owner_id = ?", ownerID).
		Order("name ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, dbError(err)
	}
	sets := make([]ExecTargetSet, len(rows))
	if len(rows) == 0 {
		return sets, nil
	}
	ids := make([]string, len(rows))
	for index, row := range rows {
		ids[index] = row.ID
		sets[index] = ExecTargetSet{ID: row.ID, Name: row.Name, SessionIDs: []string{}, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	}
	items := []ExecTargetSetItemRow{}
	if err := s.db.WithContext(ctx).
		Where("owner_id = ? AND target_set_id IN ?", ownerID, ids).
		Order("target_set_id ASC, position ASC, session_id ASC").
		Find(&items).Error; err != nil {
		return nil, dbError(err)
	}
	positions := make(map[string]int, len(rows))
	for index, row := range rows {
		positions[row.ID] = index
	}
	for _, item := range items {
		if index, ok := positions[item.TargetSetID]; ok {
			sets[index].SessionIDs = append(sets[index].SessionIDs, item.SessionID)
		}
	}
	return sets, nil
}

func (s *Store) ExecTargetSetGet(ctx context.Context, id string) (ExecTargetSet, error) {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return ExecTargetSet{}, badParam(fmt.Errorf("invalid exec target set reference"))
	}
	ownerID := ipc.OwnerID(ctx)
	row, err := execTargetSetRowByID(ctx, s.db, ownerID, id)
	if err != nil {
		return ExecTargetSet{}, err
	}
	items := []ExecTargetSetItemRow{}
	if err := s.db.WithContext(ctx).
		Where("owner_id = ? AND target_set_id = ?", ownerID, id).
		Order("position ASC, session_id ASC").
		Find(&items).Error; err != nil {
		return ExecTargetSet{}, dbError(err)
	}
	sessionIDs := make([]string, len(items))
	for index, item := range items {
		sessionIDs[index] = item.SessionID
	}
	return ExecTargetSet{ID: row.ID, Name: row.Name, SessionIDs: sessionIDs, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func (s *Store) ExecTargetSetUpdate(ctx context.Context, id, name string, sessionIDs []string) error {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return badParam(fmt.Errorf("invalid exec target set reference"))
	}
	trimmed, err := validateExecTargetSetName(name)
	if err != nil {
		return badParam(err)
	}
	if err := validateExecTargetSetSessions(sessionIDs); err != nil {
		return badParam(err)
	}
	ownerID := ipc.OwnerID(ctx)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := execTargetSetRowByID(ctx, tx, ownerID, id); err != nil {
			return err
		}
		result := tx.WithContext(ctx).Model(&ExecTargetSetRow{}).
			Where("owner_id = ? AND id = ?", ownerID, id).
			Updates(map[string]any{"name": trimmed, "updated_at": ids.NowMS()})
		if result.Error != nil {
			return result.Error
		}
		if err := tx.WithContext(ctx).
			Where("owner_id = ? AND target_set_id = ?", ownerID, id).
			Delete(&ExecTargetSetItemRow{}).Error; err != nil {
			return err
		}
		return createExecTargetSetItems(ctx, tx, ownerID, id, sessionIDs)
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

// Target sets are local-only in this version, so deletes leave no sync tombstone.
func (s *Store) ExecTargetSetDelete(ctx context.Context, id string) error {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return badParam(fmt.Errorf("invalid exec target set reference"))
	}
	ownerID := ipc.OwnerID(ctx)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := execTargetSetRowByID(ctx, tx, ownerID, id); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).
			Where("owner_id = ? AND target_set_id = ?", ownerID, id).
			Delete(&ExecTargetSetItemRow{}).Error; err != nil {
			return err
		}
		return tx.WithContext(ctx).
			Where("owner_id = ? AND id = ?", ownerID, id).
			Delete(&ExecTargetSetRow{}).Error
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func execTargetSetRowByID(ctx context.Context, db *gorm.DB, ownerID, id string) (*ExecTargetSetRow, error) {
	row := &ExecTargetSetRow{}
	err := db.WithContext(ctx).
		Where("owner_id = ? AND id = ?", ownerID, id).
		First(row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, notFound("目标集")
	}
	if err != nil {
		return nil, dbError(err)
	}
	return row, nil
}

func createExecTargetSetItems(ctx context.Context, tx *gorm.DB, ownerID, targetSetID string, sessionIDs []string) error {
	rows := make([]*ExecTargetSetItemRow, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		rows[index] = &ExecTargetSetItemRow{
			OwnerID: ownerID, TargetSetID: targetSetID, SessionID: sessionID, Position: int32(index),
		}
	}
	return tx.WithContext(ctx).CreateInBatches(rows, 64).Error
}

func validateExecTargetSetName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("exec target set name is required")
	}
	if len(trimmed) > ExecTargetSetMaxNameBytes {
		return "", fmt.Errorf("exec target set name exceeds %d bytes", ExecTargetSetMaxNameBytes)
	}
	return trimmed, nil
}

func validateExecTargetSetSessions(sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return fmt.Errorf("exec target set requires at least one session")
	}
	if len(sessionIDs) > ExecTargetSetMaxTargets {
		return fmt.Errorf("exec target set exceeds %d targets", ExecTargetSetMaxTargets)
	}
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID == "" || len(sessionID) > execTargetSetMaxSessionIDLen || strings.TrimSpace(sessionID) != sessionID {
			return fmt.Errorf("invalid exec target set session id")
		}
		if _, exists := seen[sessionID]; exists {
			return fmt.Errorf("duplicate exec target set session %q", sessionID)
		}
		seen[sessionID] = struct{}{}
	}
	return nil
}
