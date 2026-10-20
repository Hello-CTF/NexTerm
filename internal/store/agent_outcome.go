package store

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

type AgentOutcomeRow = model.AgentOutcomeRow

// Agent outcome lifecycle states. Initiated is the in-progress state;
// succeeded and failed are the final states. Completion is monotonic:
// a final record is never regressed.
const (
	AgentOutcomeStateInitiated = "initiated"
	AgentOutcomeStateSucceeded = "succeeded"
	AgentOutcomeStateFailed    = "failed"
)

// AgentOutcomeFinal reports whether the state is terminal.
func AgentOutcomeFinal(state string) bool {
	return state == AgentOutcomeStateSucceeded || state == AgentOutcomeStateFailed
}

// AgentOutcomeBegin inserts the initiated record for one mutation attempt.
// An existing record is returned unchanged with created=false: beginning
// twice never rewrites the original attempt's record.
func (s *Store) AgentOutcomeBegin(ctx context.Context, row AgentOutcomeRow) (AgentOutcomeRow, bool, error) {
	if strings.TrimSpace(row.OperationID) == "" {
		return AgentOutcomeRow{}, false, ipc.NewError(ipc.CodeBadParam, "outcome operation_id 不能为空")
	}
	if strings.TrimSpace(row.Kind) == "" {
		return AgentOutcomeRow{}, false, ipc.NewError(ipc.CodeBadParam, "outcome kind 不能为空")
	}
	if row.State != AgentOutcomeStateInitiated {
		return AgentOutcomeRow{}, false, ipc.NewError(ipc.CodeBadParam, "outcome 必须以 initiated 开始")
	}
	row.OwnerID = ipc.OwnerID(ctx)
	now := ids.NowMS()
	row.CreatedAt = now
	row.UpdatedAt = now
	result := s.db.WithContext(ctx).Create(&row)
	if result.Error != nil {
		if IsUniqueErr(result.Error) {
			var stored AgentOutcomeRow
			if err := s.db.WithContext(ctx).Where("owner_id = ? AND operation_id = ?", row.OwnerID, row.OperationID).First(&stored).Error; err != nil {
				return AgentOutcomeRow{}, false, dbError(err)
			}
			return stored, false, nil
		}
		return AgentOutcomeRow{}, false, dbError(result.Error)
	}
	return row, true, nil
}

// AgentOutcomeGet returns the owner-scoped outcome record: the result query
// callers use to fetch the original operation's outcome before retrying.
func (s *Store) AgentOutcomeGet(ctx context.Context, operationID string) (AgentOutcomeRow, error) {
	var row AgentOutcomeRow
	err := s.db.WithContext(ctx).Model(&AgentOutcomeRow{}).
		Where("owner_id = ? AND operation_id = ?", ipc.OwnerID(ctx), operationID).First(&row).Error
	if isNoRows(err) {
		return AgentOutcomeRow{}, notFound("outcome 记录")
	}
	if err != nil {
		return AgentOutcomeRow{}, dbError(err)
	}
	return row, nil
}

// AgentOutcomeListByPrefix returns the owner's outcome rows whose
// operation ID starts with prefix, newest first (created_at DESC,
// operation_id DESC). The prefix is a literal string, never a LIKE
// pattern: pattern characters are escaped, so a caller-supplied prefix can
// only narrow the match. The (owner_id, operation_id) primary key makes
// the lookup an indexed range scan on every supported backend. An empty
// prefix would enumerate the owner's whole ledger and is rejected.
func (s *Store) AgentOutcomeListByPrefix(ctx context.Context, prefix string, limit int) ([]AgentOutcomeRow, error) {
	if prefix == "" {
		return nil, ipc.NewError(ipc.CodeBadParam, "outcome 前缀不能为空")
	}
	if limit <= 0 {
		limit = agentOutcomePrefixDefaultLimit
	}
	if limit > agentOutcomePrefixMaxLimit {
		limit = agentOutcomePrefixMaxLimit
	}
	var rows []AgentOutcomeRow
	err := s.db.WithContext(ctx).Model(&AgentOutcomeRow{}).
		Where("owner_id = ? AND operation_id LIKE ? ESCAPE '!'", ipc.OwnerID(ctx), escapeOutcomePrefix(prefix)+"%").
		Order("created_at DESC, operation_id DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, dbError(err)
	}
	return rows, nil
}

const (
	agentOutcomePrefixDefaultLimit = 64
	agentOutcomePrefixMaxLimit     = 256
)

// escapeOutcomePrefix escapes the LIKE pattern characters (and the escape
// character itself) so the prefix matches literally on every backend.
func escapeOutcomePrefix(prefix string) string {
	var escaped strings.Builder
	escaped.Grow(len(prefix))
	for _, r := range prefix {
		switch r {
		case '!', '%', '_':
			escaped.WriteByte('!')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}

// AgentOutcomeComplete moves an initiated record to a final state. The
// compare-and-swap on the in-progress state makes concurrent completions
// safe: exactly one wins, and a record that is already final is returned
// unchanged, so the original outcome can never regress.
func (s *Store) AgentOutcomeComplete(ctx context.Context, operationID string, updates map[string]any) (AgentOutcomeRow, error) {
	state, _ := updates["state"].(string)
	if !AgentOutcomeFinal(state) {
		return AgentOutcomeRow{}, ipc.NewError(ipc.CodeBadParam, "outcome 完成状态必须是 succeeded 或 failed")
	}
	if len(updates) == 0 {
		return AgentOutcomeRow{}, ipc.NewError(ipc.CodeBadParam, "outcome 完成内容不能为空")
	}
	updates["updated_at"] = ids.NowMS()
	result := s.db.WithContext(ctx).Model(&AgentOutcomeRow{}).
		Where("owner_id = ? AND operation_id = ? AND state = ?", ipc.OwnerID(ctx), operationID, AgentOutcomeStateInitiated).
		Updates(updates)
	if result.Error != nil {
		return AgentOutcomeRow{}, dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		var stored AgentOutcomeRow
		err := s.db.WithContext(ctx).Model(&AgentOutcomeRow{}).
			Where("owner_id = ? AND operation_id = ?", ipc.OwnerID(ctx), operationID).First(&stored).Error
		if isNoRows(err) {
			return AgentOutcomeRow{}, notFound("outcome 记录")
		}
		if err != nil {
			return AgentOutcomeRow{}, dbError(err)
		}
		// Already final: the original outcome stands (monotonic completion).
		return stored, nil
	}
	return s.AgentOutcomeGet(ctx, operationID)
}
