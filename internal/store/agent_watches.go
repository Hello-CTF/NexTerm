package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm"
)

// Agent watch persistence. The watch condition row and its outbox event are
// written in one transaction, so a committed fire always has exactly one
// outbox row and a committed cancel settles the pending row atomically.
// Delivery is at-least-once: the fixed DeliveryID is the consumer-side
// idempotency key, and CAS on Revision serializes writers without promising
// exactly-once delivery.
const (
	AgentWatchStatusArmed        = "armed"
	AgentWatchStatusMatched      = "matched"
	AgentWatchStatusCancelled    = "cancelled"
	AgentWatchStatusExpired      = "expired"
	AgentWatchStatusUnverifiable = "unverifiable"

	AgentWatchEventPending   = "pending"
	AgentWatchEventAcked     = "acked"
	AgentWatchEventCancelled = "cancelled"
)

var (
	ErrAgentWatchNotFound = ipc.NewError(ipc.CodeNotFound, "agent watch 不存在")
	ErrAgentWatchConflict = errors.New("agent watch revision conflict")
	ErrAgentWatchIdentity = errors.New("agent watch identity mismatch")
	ErrAgentWatchState    = errors.New("agent watch state conflict")
)

// EnsureAgentWatchTables migrates the agent watch schema on the given handle.
// The models are intentionally not part of model.AllModels yet, so the shared
// GORM generation owner can register and regenerate them in one batch; until
// then the watches store owns its idempotent migration.
func EnsureAgentWatchTables(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return badParam(fmt.Errorf("agent watch 迁移缺少数据库句柄"))
	}
	if err := db.WithContext(ctx).AutoMigrate(&model.AgentWatchRow{}, &model.AgentWatchOutboxRow{}); err != nil {
		return migrateError(err)
	}
	return nil
}

func agentWatchOwnerScope(ctx context.Context, db *gorm.DB, column string) *gorm.DB {
	return db.Where(column+" = ?", ipc.OwnerID(ctx))
}

func (s *Store) agentWatchByID(ctx context.Context, db *gorm.DB, watchID string) (model.AgentWatchRow, error) {
	var row model.AgentWatchRow
	err := agentWatchOwnerScope(ctx, db.Where("id = ?", watchID), "owner_id").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrAgentWatchNotFound
	}
	if err != nil {
		return row, dbError(err)
	}
	return row, nil
}

func (s *Store) agentWatchEventByID(ctx context.Context, db *gorm.DB, deliveryID string) (model.AgentWatchOutboxRow, error) {
	var row model.AgentWatchOutboxRow
	err := agentWatchOwnerScope(ctx, db.Where("delivery_id = ?", deliveryID), "owner_id").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrAgentWatchNotFound
	}
	if err != nil {
		return row, dbError(err)
	}
	return row, nil
}

func (s *Store) AgentWatchCreate(ctx context.Context, row model.AgentWatchRow) (model.AgentWatchRow, error) {
	if row.ID == "" {
		return model.AgentWatchRow{}, badParam(fmt.Errorf("agent watch 缺少 ID"))
	}
	if row.TargetID == "" {
		return model.AgentWatchRow{}, badParam(fmt.Errorf("agent watch 缺少目标"))
	}
	if row.Identity == "" {
		return model.AgentWatchRow{}, badParam(fmt.Errorf("agent watch 缺少身份绑定"))
	}
	if !json.Valid([]byte(row.ConditionJSON)) {
		return model.AgentWatchRow{}, badParam(fmt.Errorf("agent watch 条件不是有效 JSON"))
	}
	row.OwnerID = ipc.OwnerID(ctx)
	row.Status = AgentWatchStatusArmed
	row.Revision = 1
	row.DeliveryID = ""
	row.RunID = ""
	row.LastError = ""
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if IsUniqueErr(err) {
			return model.AgentWatchRow{}, fmt.Errorf("agent watch %s: %w", row.ID, ErrAgentWatchConflict)
		}
		return model.AgentWatchRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) AgentWatchGet(ctx context.Context, watchID string) (model.AgentWatchRow, error) {
	return s.agentWatchByID(ctx, s.db.WithContext(ctx), watchID)
}

func (s *Store) AgentWatchListByTarget(ctx context.Context, targetID string) ([]model.AgentWatchRow, error) {
	rows := []model.AgentWatchRow{}
	err := agentWatchOwnerScope(ctx, s.db.WithContext(ctx).Where("target_id = ?", targetID), "owner_id").
		Order("created_at ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, dbError(err)
	}
	return rows, nil
}

// AgentWatchAdvance moves the output anchor of an armed watch without firing.
// The anchor never regresses; identity must still match the binding.
func (s *Store) AgentWatchAdvance(ctx context.Context, watchID string, expectedRevision uint64, identity string, anchorSeq, now int64) (model.AgentWatchRow, error) {
	var updated model.AgentWatchRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		watch, err := s.agentWatchByID(ctx, tx, watchID)
		if err != nil {
			return err
		}
		if watch.Revision != expectedRevision {
			return fmt.Errorf("agent watch %s revision %d != %d: %w", watchID, watch.Revision, expectedRevision, ErrAgentWatchConflict)
		}
		if watch.Status != AgentWatchStatusArmed {
			return fmt.Errorf("agent watch %s status %s: %w", watchID, watch.Status, ErrAgentWatchState)
		}
		if watch.Identity != identity {
			return fmt.Errorf("agent watch %s identity: %w", watchID, ErrAgentWatchIdentity)
		}
		if anchorSeq < watch.AnchorSeq {
			return fmt.Errorf("agent watch %s anchor %d < %d: %w", watchID, anchorSeq, watch.AnchorSeq, ErrAgentWatchConflict)
		}
		info := tx.Model(&model.AgentWatchRow{}).
			Where("id = ? AND owner_id = ? AND revision = ?", watch.ID, watch.OwnerID, expectedRevision).
			Updates(map[string]any{"anchor_seq": anchorSeq, "revision": expectedRevision + 1, "updated_at": now})
		if info.Error != nil {
			return dbError(info.Error)
		}
		if info.RowsAffected != 1 {
			return fmt.Errorf("agent watch %s: %w", watchID, ErrAgentWatchConflict)
		}
		updated, err = s.agentWatchByID(ctx, tx, watchID)
		return err
	})
	return updated, err
}

// AgentWatchFireParams fixes the identity of one logical fire. DeliveryID and
// RunID are chosen by the caller before the first attempt and must stay
// identical across retries, so a retried fire is a no-op that returns the
// already-recorded event instead of a duplicate.
type AgentWatchFireParams struct {
	WatchID          string
	ExpectedRevision uint64
	Identity         string
	DeliveryID       string
	RunID            string
	AnchorSeq        int64
	MatchedFrom      int64
	MatchedTo        int64
	Excerpt          string
	EventKind        string
	EventJSON        string
	Now              int64
}

// AgentWatchFire transitions an armed watch to matched and inserts the outbox
// event in one transaction. The outbox primary key and the unique
// (watch_id, fire_seq) pair make the same logical fire idempotent and reject
// double fires with fresh IDs.
func (s *Store) AgentWatchFire(ctx context.Context, params AgentWatchFireParams) (model.AgentWatchOutboxRow, error) {
	if params.DeliveryID == "" || params.RunID == "" {
		return model.AgentWatchOutboxRow{}, badParam(fmt.Errorf("agent watch 触发缺少固定的 DeliveryID/RunID"))
	}
	if !json.Valid([]byte(params.EventJSON)) {
		return model.AgentWatchOutboxRow{}, badParam(fmt.Errorf("agent watch 事件不是有效 JSON"))
	}
	var out model.AgentWatchOutboxRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if existing, err := s.agentWatchEventByID(ctx, tx, params.DeliveryID); err == nil {
			if existing.WatchID != params.WatchID {
				return fmt.Errorf("delivery %s belongs to watch %s: %w", params.DeliveryID, existing.WatchID, ErrAgentWatchConflict)
			}
			out = existing
			return nil
		} else if !errors.Is(err, ErrAgentWatchNotFound) {
			return err
		}
		watch, err := s.agentWatchByID(ctx, tx, params.WatchID)
		if err != nil {
			return err
		}
		if watch.Revision != params.ExpectedRevision {
			return fmt.Errorf("agent watch %s revision %d != %d: %w", params.WatchID, watch.Revision, params.ExpectedRevision, ErrAgentWatchConflict)
		}
		if watch.Status != AgentWatchStatusArmed {
			return fmt.Errorf("agent watch %s status %s: %w", params.WatchID, watch.Status, ErrAgentWatchState)
		}
		if watch.Identity != params.Identity {
			return fmt.Errorf("agent watch %s identity: %w", params.WatchID, ErrAgentWatchIdentity)
		}
		if params.AnchorSeq < watch.AnchorSeq {
			return fmt.Errorf("agent watch %s anchor %d < %d: %w", params.WatchID, params.AnchorSeq, watch.AnchorSeq, ErrAgentWatchConflict)
		}
		fireSeq := watch.FireSeq + 1
		info := tx.Model(&model.AgentWatchRow{}).
			Where("id = ? AND owner_id = ? AND revision = ?", watch.ID, watch.OwnerID, params.ExpectedRevision).
			Updates(map[string]any{
				"status":       AgentWatchStatusMatched,
				"anchor_seq":   params.AnchorSeq,
				"fire_seq":     fireSeq,
				"delivery_id":  params.DeliveryID,
				"run_id":       params.RunID,
				"matched_from": params.MatchedFrom,
				"matched_to":   params.MatchedTo,
				"excerpt":      params.Excerpt,
				"revision":     params.ExpectedRevision + 1,
				"updated_at":   params.Now,
			})
		if info.Error != nil {
			return dbError(info.Error)
		}
		if info.RowsAffected != 1 {
			return fmt.Errorf("agent watch %s: %w", params.WatchID, ErrAgentWatchConflict)
		}
		row := model.AgentWatchOutboxRow{
			DeliveryID: params.DeliveryID,
			RunID:      params.RunID,
			WatchID:    watch.ID,
			FireSeq:    fireSeq,
			OwnerID:    watch.OwnerID,
			Kind:       params.EventKind,
			Status:     AgentWatchEventPending,
			Revision:   1,
			EventJSON:  params.EventJSON,
			CreatedAt:  params.Now,
			UpdatedAt:  params.Now,
		}
		if err := tx.Create(&row).Error; err != nil {
			if IsUniqueErr(err) {
				return fmt.Errorf("agent watch %s fire %d already recorded: %w", params.WatchID, fireSeq, ErrAgentWatchConflict)
			}
			return dbError(err)
		}
		out = row
		return nil
	})
	return out, err
}

// AgentWatchCancel settles a watch and, in the same transaction, cancels its
// still-pending outbox rows so the dispatcher stops delivering them. A
// delivery already in flight may still land; consumers dedupe by DeliveryID.
func (s *Store) AgentWatchCancel(ctx context.Context, watchID string, expectedRevision uint64, now int64) (model.AgentWatchRow, error) {
	var updated model.AgentWatchRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		watch, err := s.agentWatchByID(ctx, tx, watchID)
		if err != nil {
			return err
		}
		if watch.Status == AgentWatchStatusCancelled {
			updated = watch
			return nil
		}
		if watch.Revision != expectedRevision {
			return fmt.Errorf("agent watch %s revision %d != %d: %w", watchID, watch.Revision, expectedRevision, ErrAgentWatchConflict)
		}
		info := tx.Model(&model.AgentWatchRow{}).
			Where("id = ? AND owner_id = ? AND revision = ?", watch.ID, watch.OwnerID, expectedRevision).
			Updates(map[string]any{"status": AgentWatchStatusCancelled, "revision": expectedRevision + 1, "updated_at": now})
		if info.Error != nil {
			return dbError(info.Error)
		}
		if info.RowsAffected != 1 {
			return fmt.Errorf("agent watch %s: %w", watchID, ErrAgentWatchConflict)
		}
		if err := tx.Model(&model.AgentWatchOutboxRow{}).
			Where("watch_id = ? AND owner_id = ? AND status = ?", watch.ID, watch.OwnerID, AgentWatchEventPending).
			Updates(map[string]any{"status": AgentWatchEventCancelled, "revision": gorm.Expr("revision + 1"), "updated_at": now}).Error; err != nil {
			return dbError(err)
		}
		updated, err = s.agentWatchByID(ctx, tx, watchID)
		return err
	})
	return updated, err
}

// AgentWatchMarkUnverifiable terminally marks a watch whose output continuity
// or identity can no longer be verified; unverifiable watches never fire.
func (s *Store) AgentWatchMarkUnverifiable(ctx context.Context, watchID string, expectedRevision uint64, cause string, now int64) (model.AgentWatchRow, error) {
	var updated model.AgentWatchRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		watch, err := s.agentWatchByID(ctx, tx, watchID)
		if err != nil {
			return err
		}
		if watch.Status == AgentWatchStatusUnverifiable {
			updated = watch
			return nil
		}
		if watch.Revision != expectedRevision {
			return fmt.Errorf("agent watch %s revision %d != %d: %w", watchID, watch.Revision, expectedRevision, ErrAgentWatchConflict)
		}
		if watch.Status != AgentWatchStatusArmed {
			return fmt.Errorf("agent watch %s status %s: %w", watchID, watch.Status, ErrAgentWatchState)
		}
		info := tx.Model(&model.AgentWatchRow{}).
			Where("id = ? AND owner_id = ? AND revision = ?", watch.ID, watch.OwnerID, expectedRevision).
			Updates(map[string]any{"status": AgentWatchStatusUnverifiable, "last_error": cause, "revision": expectedRevision + 1, "updated_at": now})
		if info.Error != nil {
			return dbError(info.Error)
		}
		if info.RowsAffected != 1 {
			return fmt.Errorf("agent watch %s: %w", watchID, ErrAgentWatchConflict)
		}
		updated, err = s.agentWatchByID(ctx, tx, watchID)
		return err
	})
	return updated, err
}

// AgentWatchExpireDue settles armed watches whose expiry has passed.
func (s *Store) AgentWatchExpireDue(ctx context.Context, now int64) (int64, error) {
	info := agentWatchOwnerScope(ctx, s.db.WithContext(ctx).Model(&model.AgentWatchRow{}), "owner_id").
		Where("status = ? AND expires_at IS NOT NULL AND expires_at <= ?", AgentWatchStatusArmed, now).
		Updates(map[string]any{"status": AgentWatchStatusExpired, "revision": gorm.Expr("revision + 1"), "updated_at": now})
	if info.Error != nil {
		return 0, dbError(info.Error)
	}
	return info.RowsAffected, nil
}

func (s *Store) AgentWatchEventPendingList(ctx context.Context, limit int) ([]model.AgentWatchOutboxRow, error) {
	if limit <= 0 {
		limit = 64
	}
	rows := []model.AgentWatchOutboxRow{}
	err := agentWatchOwnerScope(ctx, s.db.WithContext(ctx).Where("status = ?", AgentWatchEventPending), "owner_id").
		Order("created_at ASC, delivery_id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, dbError(err)
	}
	return rows, nil
}

func (s *Store) AgentWatchEventGet(ctx context.Context, deliveryID string) (model.AgentWatchOutboxRow, error) {
	return s.agentWatchEventByID(ctx, s.db.WithContext(ctx), deliveryID)
}

// AgentWatchEventAck marks a pending event acked and, in the same
// transaction, re-arms a repeat watch so it can fire again from its current
// anchor. A duplicate Ack with a stale revision is an idempotent no-op once
// the row is acked; concurrent acks are serialized by CAS.
func (s *Store) AgentWatchEventAck(ctx context.Context, deliveryID string, expectedRevision uint64, now int64) (model.AgentWatchOutboxRow, error) {
	var updated model.AgentWatchOutboxRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		event, err := s.agentWatchEventByID(ctx, tx, deliveryID)
		if err != nil {
			return err
		}
		if event.Status == AgentWatchEventAcked {
			updated = event
			return nil
		}
		if event.Status != AgentWatchEventPending {
			return fmt.Errorf("agent watch event %s status %s: %w", deliveryID, event.Status, ErrAgentWatchState)
		}
		if event.Revision != expectedRevision {
			return fmt.Errorf("agent watch event %s revision %d != %d: %w", deliveryID, event.Revision, expectedRevision, ErrAgentWatchConflict)
		}
		info := tx.Model(&model.AgentWatchOutboxRow{}).
			Where("delivery_id = ? AND owner_id = ? AND revision = ? AND status = ?", event.DeliveryID, event.OwnerID, expectedRevision, AgentWatchEventPending).
			Updates(map[string]any{"status": AgentWatchEventAcked, "delivered_at": now, "revision": expectedRevision + 1, "updated_at": now})
		if info.Error != nil {
			return dbError(info.Error)
		}
		if info.RowsAffected != 1 {
			return fmt.Errorf("agent watch event %s: %w", deliveryID, ErrAgentWatchConflict)
		}
		watch, err := s.agentWatchByID(ctx, tx, event.WatchID)
		if err != nil {
			return err
		}
		if watch.Repeat && watch.Status == AgentWatchStatusMatched && watch.DeliveryID == event.DeliveryID {
			info := tx.Model(&model.AgentWatchRow{}).
				Where("id = ? AND owner_id = ? AND revision = ?", watch.ID, watch.OwnerID, watch.Revision).
				Updates(map[string]any{
					"status":       AgentWatchStatusArmed,
					"delivery_id":  "",
					"run_id":       "",
					"matched_from": 0,
					"matched_to":   0,
					"excerpt":      "",
					"revision":     watch.Revision + 1,
					"updated_at":   now,
				})
			if info.Error != nil {
				return dbError(info.Error)
			}
			if info.RowsAffected != 1 {
				return fmt.Errorf("agent watch %s re-arm: %w", watch.ID, ErrAgentWatchConflict)
			}
		}
		updated, err = s.agentWatchEventByID(ctx, tx, deliveryID)
		return err
	})
	return updated, err
}
