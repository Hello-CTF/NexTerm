package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

type AgentRosterRow = model.AgentRosterRow
type AgentRosterIdentity = model.AgentRosterIdentity

// ErrAgentRosterIdentityConflict reports an update whose ID+CreatedAt+Incarnation triple
// no longer matches the stored row: the row was replaced or re-provisioned underneath
// the caller.
var ErrAgentRosterIdentityConflict = errors.New("agent roster 精确身份不匹配")

// ErrAgentRosterStateConflict reports a compare-and-swap whose expected state no
// longer matches the stored row: another writer transitioned it first.
var ErrAgentRosterStateConflict = errors.New("agent roster 状态已变化")

func rosterOwnerID(ctx context.Context) string {
	return ipc.OwnerID(ctx)
}

// rosterTimestamp fills updated_at unless the caller pinned it: idempotent
// retries carry the original timestamp instead of bumping it, which also
// makes the zero-row no-op path observable on MySQL-family drivers.
func rosterTimestamp(updates map[string]any) {
	if _, ok := updates["updated_at"]; !ok {
		updates["updated_at"] = ids.NowMS()
	}
}

// AgentRosterProvision inserts a roster row provisioning-first: the row (with its
// preassigned TabID and fresh Incarnation) exists before any runtime is started.
// Handles are permanent per owner and never reused; a duplicate handle is a conflict.
func (s *Store) AgentRosterProvision(ctx context.Context, row AgentRosterRow) (AgentRosterRow, error) {
	if strings.TrimSpace(row.ID) == "" {
		row.ID = ids.New()
	}
	if strings.TrimSpace(row.Handle) == "" {
		return AgentRosterRow{}, ipc.NewError(ipc.CodeBadParam, "roster handle 不能为空")
	}
	if strings.TrimSpace(row.State) == "" {
		return AgentRosterRow{}, ipc.NewError(ipc.CodeBadParam, "roster state 不能为空")
	}
	if strings.TrimSpace(row.Incarnation) == "" {
		return AgentRosterRow{}, ipc.NewError(ipc.CodeBadParam, "roster incarnation 不能为空")
	}
	row.OwnerID = rosterOwnerID(ctx)
	if row.MetadataJSON == "" {
		row.MetadataJSON = "{}"
	}
	// Metadata must be a JSON object: null, arrays, and scalars are
	// rejected at the store boundary so later merges never face them.
	var metadataObject map[string]any
	if err := json.Unmarshal([]byte(row.MetadataJSON), &metadataObject); err != nil || metadataObject == nil {
		return AgentRosterRow{}, ipc.NewError(ipc.CodeBadParam, "roster metadata 必须是 JSON 对象")
	}
	now := ids.NowMS()
	if row.CreatedAt == 0 {
		row.CreatedAt = now
	}
	row.UpdatedAt = now
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if IsUniqueErr(err) {
			return AgentRosterRow{}, ipc.NewError(ipc.Code("conflict"), "roster handle 已存在")
		}
		return AgentRosterRow{}, dbError(err)
	}
	return row, nil
}

// AgentRosterGet returns the owner-scoped row by ID.
func (s *Store) AgentRosterGet(ctx context.Context, id string) (AgentRosterRow, error) {
	var row AgentRosterRow
	err := s.db.WithContext(ctx).Model(&AgentRosterRow{}).Where("id = ? AND owner_id = ?", id, rosterOwnerID(ctx)).First(&row).Error
	if isNoRows(err) {
		return AgentRosterRow{}, notFound("roster 条目")
	}
	if err != nil {
		return AgentRosterRow{}, dbError(err)
	}
	return row, nil
}

// AgentRosterGetByHandle resolves the permanent handle to the owner-scoped row,
// enabling cross-job resolution: the same handle points at the current entry
// no matter which job last bound to it.
func (s *Store) AgentRosterGetByHandle(ctx context.Context, handle string) (AgentRosterRow, error) {
	var row AgentRosterRow
	err := s.db.WithContext(ctx).Model(&AgentRosterRow{}).Where("handle = ? AND owner_id = ?", handle, rosterOwnerID(ctx)).First(&row).Error
	if isNoRows(err) {
		return AgentRosterRow{}, notFound("roster 条目")
	}
	if err != nil {
		return AgentRosterRow{}, dbError(err)
	}
	return row, nil
}

// AgentRosterList returns the owner's roster rows for a conversation, oldest first.
// An empty conversationID lists every row of the owner (orphan reconciliation).
func (s *Store) AgentRosterList(ctx context.Context, conversationID string) ([]AgentRosterRow, error) {
	query := s.db.WithContext(ctx).Model(&AgentRosterRow{}).Where("owner_id = ?", rosterOwnerID(ctx))
	if conversationID != "" {
		query = query.Where("conversation_id = ?", conversationID)
	}
	rows := []AgentRosterRow{}
	if err := query.Order("created_at").Order("id").Find(&rows).Error; err != nil {
		return nil, dbError(err)
	}
	return rows, nil
}

// AgentRosterUpdate applies updates only when the exact durable identity
// (ID+CreatedAt+Incarnation) still matches; a mismatch returns
// ErrAgentRosterIdentityConflict instead of writing to a replacement row.
func (s *Store) AgentRosterUpdate(ctx context.Context, id string, identity AgentRosterIdentity, updates map[string]any) (AgentRosterRow, error) {
	if len(updates) == 0 {
		return s.AgentRosterGet(ctx, id)
	}
	rosterTimestamp(updates)
	result := s.db.WithContext(ctx).Model(&AgentRosterRow{}).
		Where("id = ? AND owner_id = ? AND created_at = ? AND incarnation = ?", id, rosterOwnerID(ctx), identity.CreatedAt, identity.Incarnation).
		Updates(updates)
	if result.Error != nil {
		return AgentRosterRow{}, dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		// MySQL-family drivers report changed rows, not matched rows: an
		// idempotent no-op update is not an identity conflict. Re-read and
		// compare the complete identity before classifying.
		current, err := s.AgentRosterGet(ctx, id)
		if err != nil {
			return AgentRosterRow{}, err
		}
		if current.Incarnation != identity.Incarnation || current.CreatedAt != identity.CreatedAt {
			return AgentRosterRow{}, ErrAgentRosterIdentityConflict
		}
		return current, nil
	}
	return s.AgentRosterGet(ctx, id)
}

// AgentRosterCompareAndSwapState applies updates only when the exact durable
// identity matches AND the row is still in expectedState, closing the
// read-check-write race between concurrent lifecycle transitions. A state
// mismatch returns ErrAgentRosterStateConflict without writing.
func (s *Store) AgentRosterCompareAndSwapState(ctx context.Context, id string, identity AgentRosterIdentity, expectedState string, updates map[string]any) (AgentRosterRow, error) {
	if len(updates) == 0 {
		return s.AgentRosterGet(ctx, id)
	}
	rosterTimestamp(updates)
	result := s.db.WithContext(ctx).Model(&AgentRosterRow{}).
		Where("id = ? AND owner_id = ? AND created_at = ? AND incarnation = ? AND state = ?", id, rosterOwnerID(ctx), identity.CreatedAt, identity.Incarnation, expectedState).
		Updates(updates)
	if result.Error != nil {
		return AgentRosterRow{}, dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		current, err := s.AgentRosterGet(ctx, id)
		if err != nil {
			return AgentRosterRow{}, err
		}
		if current.Incarnation != identity.Incarnation || current.CreatedAt != identity.CreatedAt {
			return AgentRosterRow{}, ErrAgentRosterIdentityConflict
		}
		if current.State != expectedState {
			return AgentRosterRow{}, ErrAgentRosterStateConflict
		}
		// A genuine no-op (identical values, same-millisecond timestamp) is
		// a success, not a conflict.
		return current, nil
	}
	return s.AgentRosterGet(ctx, id)
}

// Roster rows are never hard-deleted: the gone/replaced/exited terminal
// states are the tombstones that keep orphan evidence auditable and keep the
// permanent owner+handle key from ever being reused. Authoritative absence
// is established by the runtime probe (transition to gone), not by removal.
