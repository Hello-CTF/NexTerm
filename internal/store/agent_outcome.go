package store

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm"
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
			if err := s.inEvidenceWinnerRead(ctx, func() error {
				return s.db.WithContext(ctx).Where("owner_id = ? AND operation_id = ?", row.OwnerID, row.OperationID).First(&stored).Error
			}); err != nil {
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

// agentOutcomeClaimSuffix marks the single in-flight attempt row of an
// operation family: the (owner_id, operation_id) primary key makes the
// claim insert atomic, so concurrent claimants on any supported backend
// serialize into exactly one winner without a schema change.
const agentOutcomeClaimSuffix = ":claim"

// AgentOutcomeClaimRowID returns the in-flight claim row's operation ID for
// one operation family.
func AgentOutcomeClaimRowID(family string) string {
	return family + agentOutcomeClaimSuffix
}

// AgentOutcomeClaimAttempt atomically claims one operation family for a new
// attempt by inserting the family's single in-flight row. It reports whether
// this caller won the claim: a false result with a nil error means another
// attempt already holds (or crashed while holding) the family, and the
// caller must adjudicate the existing records instead of starting a new
// attempt. The random-nonce attempt history rows are written separately by
// AgentOutcomeCompleteAttempt.
func (s *Store) AgentOutcomeClaimAttempt(ctx context.Context, family, kind string) (bool, error) {
	if strings.TrimSpace(family) == "" {
		return false, ipc.NewError(ipc.CodeBadParam, "outcome family 不能为空")
	}
	if strings.TrimSpace(kind) == "" {
		return false, ipc.NewError(ipc.CodeBadParam, "outcome kind 不能为空")
	}
	row := AgentOutcomeRow{
		OperationID: AgentOutcomeClaimRowID(family), Kind: kind, State: AgentOutcomeStateInitiated,
		OwnerID: ipc.OwnerID(ctx),
	}
	now := ids.NowMS()
	row.CreatedAt = now
	row.UpdatedAt = now
	result := s.db.WithContext(ctx).Create(&row)
	if result.Error != nil {
		if IsUniqueErr(result.Error) {
			return false, nil
		}
		return false, dbError(result.Error)
	}
	return true, nil
}

// AgentOutcomeProgress persists the attempt's progress residual on the
// family's in-flight row the moment a connect returns, so a crash before
// the final completion still leaves the residual session recoverable. The
// compare-and-swap on the in-progress state refuses to write a released or
// completed claim.
func (s *Store) AgentOutcomeProgress(ctx context.Context, family, residualSessionID string) error {
	if strings.TrimSpace(family) == "" {
		return ipc.NewError(ipc.CodeBadParam, "outcome family 不能为空")
	}
	result := s.db.WithContext(ctx).Model(&AgentOutcomeRow{}).
		Where("owner_id = ? AND operation_id = ? AND state = ?", ipc.OwnerID(ctx), AgentOutcomeClaimRowID(family), AgentOutcomeStateInitiated).
		Updates(map[string]any{"residual_session_id": residualSessionID, "updated_at": ids.NowMS()})
	if result.Error != nil {
		return dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		return notFound("outcome 进行中的尝试记录")
	}
	return nil
}

// AgentOutcomeReleaseClaim deletes the family's in-flight claim row without
// writing a history row, for the abort path where the caller claimed the
// family but must stand down — for example because a terminal record landed
// between its adjudication and its claim.
func (s *Store) AgentOutcomeReleaseClaim(ctx context.Context, family string) error {
	if strings.TrimSpace(family) == "" {
		return ipc.NewError(ipc.CodeBadParam, "outcome family 不能为空")
	}
	result := s.db.WithContext(ctx).Where("owner_id = ? AND operation_id = ?", ipc.OwnerID(ctx), AgentOutcomeClaimRowID(family)).
		Delete(&AgentOutcomeRow{})
	if result.Error != nil {
		return dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		return notFound("outcome 进行中的尝试记录")
	}
	return nil
}

// AgentOutcomeCompleteAttempt records the attempt's terminal history row
// and releases the family claim in one transaction: the history row keeps
// the random-nonce attempt ID and its full payload, and the claim row's
// deletion admits the family's next attempt. A crash between the two would
// leave the claim held, which blocks the next attempt instead of letting
// it double-execute.
func (s *Store) AgentOutcomeCompleteAttempt(ctx context.Context, family string, row AgentOutcomeRow) error {
	if strings.TrimSpace(family) == "" {
		return ipc.NewError(ipc.CodeBadParam, "outcome family 不能为空")
	}
	if strings.TrimSpace(row.OperationID) == "" {
		return ipc.NewError(ipc.CodeBadParam, "outcome operation_id 不能为空")
	}
	if strings.TrimSpace(row.Kind) == "" {
		return ipc.NewError(ipc.CodeBadParam, "outcome kind 不能为空")
	}
	if !AgentOutcomeFinal(row.State) {
		return ipc.NewError(ipc.CodeBadParam, "outcome 完成状态必须是 succeeded 或 failed")
	}
	row.OwnerID = ipc.OwnerID(ctx)
	now := ids.NowMS()
	row.CreatedAt = now
	row.UpdatedAt = now
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if result := tx.Create(&row); result.Error != nil {
			if IsUniqueErr(result.Error) {
				stored, err := s.AgentOutcomeGet(ctx, row.OperationID)
				if err != nil {
					return dbError(result.Error)
				}
				if stored.State != row.State || stored.ResultJSON != row.ResultJSON {
					return ipc.NewError(ipc.CodeInternal, "outcome 尝试记录已存在且内容不一致")
				}
			} else {
				return dbError(result.Error)
			}
		}
		if result := tx.Where("owner_id = ? AND operation_id = ?", ipc.OwnerID(ctx), AgentOutcomeClaimRowID(family)).
			Delete(&AgentOutcomeRow{}); result.Error != nil {
			return dbError(result.Error)
		}
		return nil
	})
}
