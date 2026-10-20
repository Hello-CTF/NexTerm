package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

	// AgentWatchMaxCarryBytes hard-bounds the persisted partial-match overlap
	// so a hostile or buggy writer cannot grow watch rows without limit.
	AgentWatchMaxCarryBytes = 65536
	// agentWatchMaxContextBytes hard-bounds the persisted left-context bytes
	// that ground line/word-boundary assertions.
	agentWatchMaxContextBytes = 256
)

var (
	ErrAgentWatchNotFound = ipc.NewError(ipc.CodeNotFound, "agent watch 不存在")
	ErrAgentWatchConflict = errors.New("agent watch revision conflict")
	ErrAgentWatchIdentity = errors.New("agent watch identity mismatch")
	ErrAgentWatchState    = errors.New("agent watch state conflict")
	ErrAgentWatchDetached = errors.New("agent watch target detached")
)

func agentWatchOwnerScope(ctx context.Context, db *gorm.DB, column string) *gorm.DB {
	return db.Where(column+" = ?", ipc.OwnerID(ctx))
}

// agentWatchTargetForUpdate locks the durable target row inside the caller's
// transaction. SQLite serializes writers through the immediate transaction,
// so the explicit row lock is only issued on the other backends.
func (s *Store) agentWatchTargetForUpdate(ctx context.Context, db *gorm.DB, targetID string) (model.AgentWatchTargetRow, bool, error) {
	var row model.AgentWatchTargetRow
	query := agentWatchOwnerScope(ctx, db.Where("target_id = ?", targetID), "owner_id")
	if s.backend != BackendSQLite {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, false, nil
	}
	if err != nil {
		return row, false, dbError(err)
	}
	return row, true, nil
}

// AgentWatchTargetAttach durably binds the target to an identity incarnation
// and returns the current generation. Attaching with a different identity —
// or reattaching after a permanent detach — settles every armed watch of the
// previous incarnation and bumps the durable generation in the same
// transaction, so the invalidation survives restarts and stale attachments
// cannot resurrect old watches.
func (s *Store) AgentWatchTargetAttach(ctx context.Context, targetID, identity string, now int64) (model.AgentWatchTargetRow, error) {
	if targetID == "" || identity == "" {
		return model.AgentWatchTargetRow{}, badParam(fmt.Errorf("agent watch 目标与身份不能为空"))
	}
	var out model.AgentWatchTargetRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, found, err := s.agentWatchTargetForUpdate(ctx, tx, targetID)
		if err != nil {
			return err
		}
		if !found {
			out = model.AgentWatchTargetRow{TargetID: targetID, OwnerID: ipc.OwnerID(ctx), Identity: identity, Generation: 1, UpdatedAt: now}
			if err := tx.Create(&out).Error; err != nil {
				if IsUniqueErr(err) {
					return fmt.Errorf("agent watch target %s: %w", targetID, ErrAgentWatchConflict)
				}
				return dbError(err)
			}
			return nil
		}
		if row.Identity == identity && !row.Gone {
			out = row
			return nil
		}
		generation := row.Generation + 1
		if err := s.settleAgentWatchTargetWatches(ctx, tx, targetID, row.OwnerID, generation, "target identity replaced", now); err != nil {
			return err
		}
		if err := tx.Model(&model.AgentWatchTargetRow{}).
			Where("target_id = ? AND owner_id = ?", targetID, row.OwnerID).
			Updates(map[string]any{"identity": identity, "generation": generation, "gone": false, "updated_at": now}).Error; err != nil {
			return dbError(err)
		}
		row.Identity, row.Generation, row.Gone, row.UpdatedAt = identity, generation, false, now
		out = row
		return nil
	})
	return out, err
}

// AgentWatchTargetDetach permanently detaches the target. The presenter must
// present the generation credential it obtained from Attach: a stale
// presenter — same identity string or not — can never settle the current
// incarnation. The detach is durable: the target row records the gone state
// and a bumped generation, so after a restart the detach is still honored.
// Store failures roll the transaction back and propagate as retryable
// errors — a detach never reports success while watches remain armed.
func (s *Store) AgentWatchTargetDetach(ctx context.Context, targetID, identity string, generation uint64, now int64) (bool, error) {
	settled := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, found, err := s.agentWatchTargetForUpdate(ctx, tx, targetID)
		if err != nil {
			return err
		}
		if !found || row.Identity != identity || row.Gone || row.Generation != generation {
			return nil
		}
		nextGeneration := row.Generation + 1
		if err := s.settleAgentWatchTargetWatches(ctx, tx, targetID, row.OwnerID, nextGeneration, "target detached permanently", now); err != nil {
			return err
		}
		if err := tx.Model(&model.AgentWatchTargetRow{}).
			Where("target_id = ? AND owner_id = ?", targetID, row.OwnerID).
			Updates(map[string]any{"generation": nextGeneration, "gone": true, "updated_at": now}).Error; err != nil {
			return dbError(err)
		}
		settled = true
		return nil
	})
	return settled, err
}

func (s *Store) AgentWatchTargetGet(ctx context.Context, targetID string) (model.AgentWatchTargetRow, error) {
	var row model.AgentWatchTargetRow
	err := agentWatchOwnerScope(ctx, s.db.WithContext(ctx).Where("target_id = ?", targetID), "owner_id").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrAgentWatchNotFound
	}
	if err != nil {
		return row, dbError(err)
	}
	return row, nil
}

// settleAgentWatchTargetWatches marks every armed watch of an older
// generation unverifiable; watches of the current incarnation never match
// output they cannot vouch for.
func (s *Store) settleAgentWatchTargetWatches(ctx context.Context, tx *gorm.DB, targetID, ownerID string, generation uint64, cause string, now int64) error {
	return tx.Model(&model.AgentWatchRow{}).
		Where("target_id = ? AND owner_id = ? AND status = ? AND generation < ?", targetID, ownerID, AgentWatchStatusArmed, generation).
		Updates(map[string]any{"status": AgentWatchStatusUnverifiable, "last_error": cause, "revision": gorm.Expr("revision + 1"), "updated_at": now}).Error
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

// AgentWatchCreate inserts an armed watch bound to the target's current
// durable identity and generation. The presenter must present the generation
// credential it obtained from Attach; creation locks the target row, so it
// serializes with permanent invalidation and a stale presenter fails with
// ErrAgentWatchDetached instead of binding to an incarnation it never
// attached to.
func (s *Store) AgentWatchCreate(ctx context.Context, row model.AgentWatchRow, generation uint64) (model.AgentWatchRow, error) {
	if row.ID == "" {
		return model.AgentWatchRow{}, badParam(fmt.Errorf("agent watch 缺少 ID"))
	}
	if row.TargetID == "" {
		return model.AgentWatchRow{}, badParam(fmt.Errorf("agent watch 缺少目标"))
	}
	if !json.Valid([]byte(row.ConditionJSON)) {
		return model.AgentWatchRow{}, badParam(fmt.Errorf("agent watch 条件不是有效 JSON"))
	}
	if row.AnchorSeq == 0 && len(row.ContextBlob) != 0 || row.AnchorSeq > 0 && len(row.ContextBlob) == 0 {
		return model.AgentWatchRow{}, badParam(fmt.Errorf("agent watch 左上下文与锚点不一致"))
	}
	if len(row.ContextBlob) > agentWatchMaxContextBytes {
		return model.AgentWatchRow{}, badParam(fmt.Errorf("agent watch 左上下文超过 %d 字节", agentWatchMaxContextBytes))
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		target, found, err := s.agentWatchTargetForUpdate(ctx, tx, row.TargetID)
		if err != nil {
			return err
		}
		if !found || target.Gone || target.Generation != generation {
			return fmt.Errorf("agent watch target %s: %w", row.TargetID, ErrAgentWatchDetached)
		}
		row.OwnerID = ipc.OwnerID(ctx)
		row.Identity = target.Identity
		row.Generation = target.Generation
		row.Status = AgentWatchStatusArmed
		row.Revision = 1
		row.DeliveryID = ""
		row.RunID = ""
		row.LastError = ""
		if err := tx.Create(&row).Error; err != nil {
			if IsUniqueErr(err) {
				return fmt.Errorf("agent watch %s: %w", row.ID, ErrAgentWatchConflict)
			}
			return dbError(err)
		}
		return nil
	})
	if err != nil {
		return model.AgentWatchRow{}, err
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

func agentWatchCarry(carry []byte) ([]byte, error) {
	if len(carry) > AgentWatchMaxCarryBytes {
		return nil, badParam(fmt.Errorf("agent watch overlap 超过 %d 字节", AgentWatchMaxCarryBytes))
	}
	if len(carry) == 0 {
		return nil, nil
	}
	return carry, nil
}

func agentWatchContext(context []byte) ([]byte, error) {
	if len(context) > agentWatchMaxContextBytes {
		return nil, badParam(fmt.Errorf("agent watch 左上下文超过 %d 字节", agentWatchMaxContextBytes))
	}
	if len(context) == 0 {
		return nil, nil
	}
	return context, nil
}

// AgentWatchAdvance persists the new anchor, the bounded overlap that
// preserves partial matches across chunk boundaries, and the true left
// context that grounds assertions at the seam. The anchor never regresses;
// identity and armed state must still match.
func (s *Store) AgentWatchAdvance(ctx context.Context, watchID string, expectedRevision uint64, identity string, anchorSeq int64, carry, context []byte, now int64) (model.AgentWatchRow, error) {
	carry, err := agentWatchCarry(carry)
	if err != nil {
		return model.AgentWatchRow{}, err
	}
	context, err = agentWatchContext(context)
	if err != nil {
		return model.AgentWatchRow{}, err
	}
	var updated model.AgentWatchRow
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
			Updates(map[string]any{"anchor_seq": anchorSeq, "carry_blob": carry, "context_blob": context, "revision": expectedRevision + 1, "updated_at": now})
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

// AgentWatchConsume slides the anchor of a matched repeat watch over output
// that arrived while the fire was awaiting acknowledgement; the suspended
// watch intentionally skips matching, but consuming the stream keeps its
// anchor current so re-arming never sees a false output gap.
func (s *Store) AgentWatchConsume(ctx context.Context, watchID string, expectedRevision uint64, identity string, anchorSeq, now int64) (model.AgentWatchRow, error) {
	var updated model.AgentWatchRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		watch, err := s.agentWatchByID(ctx, tx, watchID)
		if err != nil {
			return err
		}
		if watch.Revision != expectedRevision {
			return fmt.Errorf("agent watch %s revision %d != %d: %w", watchID, watch.Revision, expectedRevision, ErrAgentWatchConflict)
		}
		if watch.Status != AgentWatchStatusMatched || !watch.Repeat {
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
			Updates(map[string]any{"anchor_seq": anchorSeq, "carry_blob": nil, "context_blob": nil, "revision": expectedRevision + 1, "updated_at": now})
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
// event in one transaction; the persisted overlap is cleared because the
// fired match can no longer participate in a future one. The outbox primary
// key and the unique (watch_id, fire_seq) pair make the same logical fire
// idempotent and reject double fires with fresh IDs.
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
				"carry_blob":   nil,
				"context_blob": nil,
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
// or identity can no longer be verified; unverifiable watches never fire. An
// armed watch always settles this way, and so does a matched repeat watch
// whose stream continuity broke while its fire awaited acknowledgement — the
// pending event stays deliverable, but the watch itself never re-arms.
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
		if watch.Status != AgentWatchStatusArmed && !(watch.Status == AgentWatchStatusMatched && watch.Repeat) {
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
			// Re-arming hands the watch back to the live incarnation: it is
			// only valid while the durable target still is the incarnation
			// the watch fired under. A permanent detach or a replacement
			// revokes that, so the acknowledgement still commits the
			// legitimately produced event but the watch is terminated in
			// the same transaction instead of re-arming.
			target, found, err := s.agentWatchTargetForUpdate(ctx, tx, watch.TargetID)
			if err != nil {
				return err
			}
			if !found || target.Gone || target.Generation != watch.Generation {
				info := tx.Model(&model.AgentWatchRow{}).
					Where("id = ? AND owner_id = ? AND revision = ?", watch.ID, watch.OwnerID, watch.Revision).
					Updates(map[string]any{
						"status":     AgentWatchStatusUnverifiable,
						"last_error": "target incarnation gone",
						"revision":   watch.Revision + 1,
						"updated_at": now,
					})
				if info.Error != nil {
					return dbError(info.Error)
				}
				if info.RowsAffected != 1 {
					return fmt.Errorf("agent watch %s terminate on ack: %w", watch.ID, ErrAgentWatchConflict)
				}
				updated, err = s.agentWatchEventByID(ctx, tx, deliveryID)
				return err
			}
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
