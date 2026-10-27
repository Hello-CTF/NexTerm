package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type StateRuleRow = model.StateRuleRow

// State rule scopes.
const (
	StateRuleScopeGlobal = "global"
	StateRuleScopeHost   = "host"
)

// MaxStateRuleNameRunes bounds a state rule's name.
const MaxStateRuleNameRunes = 128

// StateRuleInput is a state rule as the user edits or imports it. An empty
// ID creates a rule; UpdatedAt is only read by import.
type StateRuleInput struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	AssetID   string `json:"assetId,omitempty"`
	Region    string `json:"region"`
	Lines     int32  `json:"lines,omitempty"`
	Pattern   string `json:"pattern"`
	State     string `json:"state"`
	Keys      string `json:"keys,omitempty"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

// StateRuleImportResult counts what an import did. Unchanged rules already
// existed with the same or a newer UpdatedAt.
type StateRuleImportResult struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

// StateRuleFromRow is the helper form of a stored rule.
func StateRuleFromRow(row StateRuleRow) staterule.Rule {
	return staterule.Rule{
		ID: row.ID, Region: row.Region, Lines: int(row.RegionLines),
		Pattern: row.Pattern, State: row.TargetState, Keys: row.KeySequence,
	}
}

// stateRuleFieldMessages names a rule field for users.
var stateRuleFieldMessages = map[string]string{
	"id": "ID", "region": "区域", "lines": "行数", "pattern": "匹配模式",
	"state": "目标状态", "keys": "按键序列", "rules": "规则数量",
}

// stateRuleError turns a validation error into a visible parameter error.
// prefix names the rule in a batch.
func stateRuleError(prefix string, err error) error {
	var typed *staterule.Error
	if errors.As(err, &typed) {
		field := stateRuleFieldMessages[typed.Field]
		if field == "" {
			field = typed.Field
		}
		if errors.Is(err, staterule.ErrLimit) {
			return badParam(fmt.Errorf("%s%s超出上限 %d: %v", prefix, field, typed.Limit, typed.Err))
		}
		return badParam(fmt.Errorf("%s%s无效: %v", prefix, field, typed.Err))
	}
	return badParam(fmt.Errorf("%s%v", prefix, err))
}

// normalizeStateRuleInput validates a rule. The ID must already be set.
func normalizeStateRuleInput(input StateRuleInput, prefix string) (StateRuleInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.AssetID = strings.TrimSpace(input.AssetID)
	if utf8.RuneCountInString(input.Name) > MaxStateRuleNameRunes {
		return StateRuleInput{}, badParam(fmt.Errorf("%s名称不能超过 %d 个字符", prefix, MaxStateRuleNameRunes))
	}
	switch input.Scope {
	case StateRuleScopeGlobal:
		input.AssetID = ""
	case StateRuleScopeHost:
		if input.AssetID == "" {
			return StateRuleInput{}, badParam(fmt.Errorf("%s主机范围的规则需要选择主机", prefix))
		}
	default:
		return StateRuleInput{}, badParam(fmt.Errorf("%s范围无效: %q", prefix, input.Scope))
	}
	rule := staterule.Normalize(staterule.Rule{
		ID: input.ID, Region: input.Region, Lines: int(input.Lines),
		Pattern: input.Pattern, State: input.State, Keys: input.Keys,
	})
	if _, err := staterule.Validate(rule); err != nil {
		return StateRuleInput{}, stateRuleError(prefix, err)
	}
	input.Lines = int32(rule.Lines)
	if input.Name == "" {
		input.Name = clipRunes(strings.TrimSpace(input.Pattern), MaxStateRuleNameRunes)
	}
	return input, nil
}

// ValidateStateRuleInput checks a rule as StateRuleSave would, without the
// checks that need stored data (the host and the owner's limits), and
// returns it normalized. A rule without an ID is checked under a fresh one.
func ValidateStateRuleInput(input StateRuleInput) (StateRuleInput, error) {
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		input.ID = ids.New()
	} else if err := EnsureID(input.ID); err != nil {
		return StateRuleInput{}, err
	}
	return normalizeStateRuleInput(input, "")
}

func clipRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

func stateRuleAssignments(input StateRuleInput, updatedAt int64) map[string]any {
	return map[string]any{
		"name": input.Name, "scope": input.Scope, "asset_id": input.AssetID,
		"region": input.Region, "region_lines": input.Lines, "pattern": input.Pattern,
		"target_state": input.State, "key_sequence": input.Keys, "enabled": input.Enabled,
		"updated_at": updatedAt,
	}
}

// StateRuleList returns the owner's rules, oldest first.
func (s *Store) StateRuleList(ctx context.Context) ([]StateRuleRow, error) {
	rule := query.Use(s.db).StateRuleRow
	rows := []StateRuleRow{}
	err := rule.WithContext(ctx).
		Where(ownerConditions(ctx, rule.TableName())...).
		Order(rule.Position, rule.CreatedAt, rule.ID).
		Scan(&rows)
	if err != nil {
		return nil, dbError(err)
	}
	return rows, nil
}

// StateRuleSave creates a rule (empty ID) or updates the owner's rule.
// Invalid rules and rules beyond the per-owner limit are rejected.
func (s *Store) StateRuleSave(ctx context.Context, input StateRuleInput) (StateRuleRow, error) {
	input.ID = strings.TrimSpace(input.ID)
	creating := input.ID == ""
	if creating {
		input.ID = ids.New()
	} else if err := EnsureID(input.ID); err != nil {
		return StateRuleRow{}, err
	}
	normalized, err := normalizeStateRuleInput(input, "")
	if err != nil {
		return StateRuleRow{}, err
	}
	now := ids.NowMS()
	var row StateRuleRow
	err = retryStateRulePosition(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			rule := query.Use(tx).StateRuleRow
			if err := stateRuleAssetAvailable(ctx, tx, normalized, ""); err != nil {
				return err
			}
			if err := checkStateRuleLimits(ctx, tx, []StateRuleInput{normalized}); err != nil {
				return err
			}
			if creating {
				position, err := nextStateRulePosition(ctx, tx)
				if err != nil {
					return err
				}
				row = stateRuleRow(ctx, normalized, now, now)
				row.Position = position
				return rule.WithContext(ctx).Create(&row)
			}
			existing, err := rule.WithContext(ctx).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Where(rule.ID.Eq(normalized.ID)).
				Where(ownerConditions(ctx, rule.TableName())...).
				First()
			if isNoRows(err) {
				return notFound("状态规则")
			}
			if err != nil {
				return err
			}
			// Keep updated_at strictly increasing so a newer edit always wins an
			// import or a sync.
			updatedAt := max(now, existing.UpdatedAt+1)
			if _, err := rule.WithContext(ctx).
				Where(rule.ID.Eq(normalized.ID), rule.OwnerID.Eq(existing.OwnerID)).
				UpdateColumns(stateRuleAssignments(normalized, updatedAt)); err != nil {
				return err
			}
			row = stateRuleRow(ctx, normalized, existing.CreatedAt, updatedAt)
			row.OwnerID, row.Position = existing.OwnerID, existing.Position
			return nil
		})
	})
	if err != nil {
		return StateRuleRow{}, dbError(err)
	}
	return row, nil
}

func stateRuleRow(ctx context.Context, input StateRuleInput, createdAt, updatedAt int64) StateRuleRow {
	return StateRuleRow{
		ID: input.ID, OwnerID: ipc.OwnerID(ctx), Name: input.Name, Scope: input.Scope, AssetID: input.AssetID,
		Region: input.Region, RegionLines: input.Lines, Pattern: input.Pattern, TargetState: input.State,
		KeySequence: input.Keys, Enabled: input.Enabled, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

// stateRulePositionAttempts bounds the retries of a write that lost a race
// for the next position (or a new ID) to a concurrent create.
const stateRulePositionAttempts = 16

// retryStateRulePosition runs a write transaction again when it failed on a
// unique key or a lock conflict: two creates that read the same next
// position collide on the owner's unique position index, and the loser
// starts over. Every round lets at least one writer through.
func retryStateRulePosition(write func() error) error {
	var err error
	for attempt := 0; attempt < stateRulePositionAttempts; attempt++ {
		if err = write(); err == nil || !IsUniqueErr(err) && !isBusyErr(err) {
			return err
		}
	}
	return err
}

// nextStateRulePosition is the position after the owner's last rule.
func nextStateRulePosition(ctx context.Context, tx *gorm.DB) (int64, error) {
	rule := query.Use(tx).StateRuleRow
	last, err := rule.WithContext(ctx).
		Where(ownerConditions(ctx, rule.TableName())...).
		Order(rule.Position.Desc()).
		First()
	if isNoRows(err) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return last.Position + 1, nil
}

// stateRuleAssetAvailable rejects a host-scoped rule whose host is not an
// asset of the owner.
func stateRuleAssetAvailable(ctx context.Context, tx *gorm.DB, input StateRuleInput, prefix string) error {
	if input.Scope != StateRuleScopeHost {
		return nil
	}
	asset := query.Use(tx).AssetRow
	row, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(input.AssetID)).
		Where(ownerConditions(ctx, asset.TableName())...).
		First()
	if isNoRows(err) {
		return badParam(fmt.Errorf("%s规则的主机不存在或不属于当前用户", prefix))
	}
	if err != nil {
		return err
	}
	if row.DeletedAt != nil {
		return badParam(fmt.Errorf("%s规则的主机已删除", prefix))
	}
	return nil
}

// importedStateRuleID is the ID a rule another owner holds is imported
// under: derived from the owner and the original ID, so importing the same
// export again finds the copy instead of making another.
func importedStateRuleID(owner, id string) string {
	sum := sha256.Sum256([]byte("state-rule\x00" + owner + "\x00" + id))
	return strings.ToUpper(hex.EncodeToString(sum[:]))[:26]
}

// checkStateRuleLimits rejects changes that would take the owner's rules
// beyond the rule count or the compiled pattern budget of one rule set, so
// every subset pushed to a helper stays within the helper's limits.
func checkStateRuleLimits(ctx context.Context, tx *gorm.DB, changes []StateRuleInput) error {
	rule := query.Use(tx).StateRuleRow
	rows, err := rule.WithContext(ctx).Where(ownerConditions(ctx, rule.TableName())...).Order(rule.Position, rule.CreatedAt, rule.ID).Find()
	if err != nil {
		return err
	}
	merged := make([]staterule.Rule, 0, len(rows)+len(changes))
	index := make(map[string]int, len(rows))
	for _, row := range rows {
		index[row.ID] = len(merged)
		merged = append(merged, staterule.Normalize(StateRuleFromRow(*row)))
	}
	for _, change := range changes {
		value := staterule.Normalize(staterule.Rule{
			ID: change.ID, Region: change.Region, Lines: int(change.Lines),
			Pattern: change.Pattern, State: change.State, Keys: change.Keys,
		})
		if i, ok := index[change.ID]; ok {
			merged[i] = value
			continue
		}
		index[change.ID] = len(merged)
		merged = append(merged, value)
	}
	if len(merged) > staterule.MaxRules {
		return badParam(fmt.Errorf("状态规则最多 %d 条", staterule.MaxRules))
	}
	if _, err := staterule.NewSet(merged); err != nil {
		var typed *staterule.Error
		if errors.As(err, &typed) && typed.Field == "rules" {
			return badParam(fmt.Errorf("状态规则的匹配模式总体过于复杂（上限 %d）", typed.Limit))
		}
		return stateRuleError("已保存的规则: ", err)
	}
	return nil
}

// StateRuleReorder puts the owner's rules in the order of ids, which must
// name each of the owner's rules exactly once, and returns them in that
// order. Moved rules get a newer UpdatedAt so caches and other devices see
// the change.
func (s *Store) StateRuleReorder(ctx context.Context, order []string) ([]StateRuleRow, error) {
	if len(order) > staterule.MaxRules {
		return nil, badParam(fmt.Errorf("状态规则最多 %d 条", staterule.MaxRules))
	}
	wanted := make(map[string]int, len(order))
	for i, id := range order {
		id = strings.TrimSpace(id)
		if err := EnsureID(id); err != nil {
			return nil, err
		}
		if _, dup := wanted[id]; dup {
			return nil, badParam(fmt.Errorf("规则 %s 重复出现", id))
		}
		wanted[id] = i
	}
	now := ids.NowMS()
	err := retryStateRulePosition(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			rule := query.Use(tx).StateRuleRow
			rows, err := rule.WithContext(ctx).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Where(ownerConditions(ctx, rule.TableName())...).
				Find()
			if err != nil {
				return err
			}
			if len(rows) != len(wanted) {
				return badParam(errors.New("规则列表已变化，请刷新后重试"))
			}
			moved := make([]*StateRuleRow, 0, len(rows))
			for _, row := range rows {
				i, ok := wanted[row.ID]
				if !ok {
					return badParam(errors.New("规则列表已变化，请刷新后重试"))
				}
				if row.Position != int64(i+1) {
					moved = append(moved, row)
				}
			}
			// Positions are unique per owner: moved rules first step aside to
			// negative positions, which stored rules never use.
			for _, row := range moved {
				if _, err := rule.WithContext(ctx).
					Where(rule.ID.Eq(row.ID), rule.OwnerID.Eq(row.OwnerID)).
					UpdateColumns(map[string]any{"position": -int64(wanted[row.ID] + 1)}); err != nil {
					return err
				}
			}
			for _, row := range moved {
				if _, err := rule.WithContext(ctx).
					Where(rule.ID.Eq(row.ID), rule.OwnerID.Eq(row.OwnerID)).
					UpdateColumns(map[string]any{"position": int64(wanted[row.ID] + 1), "updated_at": max(now, row.UpdatedAt+1)}); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, dbError(err)
	}
	return s.StateRuleList(ctx)
}

// StateRuleDelete removes the owner's rule.
func (s *Store) StateRuleDelete(ctx context.Context, id string) error {
	if err := EnsureID(strings.TrimSpace(id)); err != nil {
		return err
	}
	rule := query.Use(s.db).StateRuleRow
	result, err := rule.WithContext(ctx).
		Where(rule.ID.Eq(strings.TrimSpace(id))).
		Where(ownerConditions(ctx, rule.TableName())...).
		Delete()
	if err != nil {
		return dbError(err)
	}
	if result.RowsAffected == 0 {
		return notFound("状态规则")
	}
	return nil
}

// StateRuleImport adds or updates rules by ID. Every rule is validated
// first and any invalid one rejects the whole import, naming the rule. A rule
// whose ID the owner already has replaces it only when its UpdatedAt is
// newer and keeps its position; new rules follow the owner's rules in the
// order of the import. A rule whose ID another owner holds is imported under
// an ID derived from the owner and that ID, so a repeated import of the same
// export updates the copy instead of adding another.
func (s *Store) StateRuleImport(ctx context.Context, inputs []StateRuleInput) (StateRuleImportResult, error) {
	if len(inputs) > staterule.MaxRules {
		return StateRuleImportResult{}, badParam(fmt.Errorf("一次最多导入 %d 条状态规则", staterule.MaxRules))
	}
	now := ids.NowMS()
	normalized := make([]StateRuleInput, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for i, input := range inputs {
		prefix := fmt.Sprintf("第 %d 条规则: ", i+1)
		input.ID = strings.TrimSpace(input.ID)
		if input.ID == "" {
			input.ID = ids.New()
		} else if !ids.Valid(input.ID) {
			return StateRuleImportResult{}, badParam(fmt.Errorf("%sID 格式不正确", prefix))
		}
		if _, dup := seen[input.ID]; dup {
			return StateRuleImportResult{}, badParam(fmt.Errorf("%sID 重复", prefix))
		}
		seen[input.ID] = struct{}{}
		if input.UpdatedAt <= 0 || input.UpdatedAt > now {
			input.UpdatedAt = now
		}
		value, err := normalizeStateRuleInput(input, prefix)
		if err != nil {
			return StateRuleImportResult{}, err
		}
		normalized[i] = value
	}
	var result StateRuleImportResult
	err := retryStateRulePosition(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result = StateRuleImportResult{}
			rule := query.Use(tx).StateRuleRow
			var creates []*StateRuleRow
			var changes []StateRuleInput
			position, err := nextStateRulePosition(ctx, tx)
			if err != nil {
				return err
			}
			owned := func(id string) (*StateRuleRow, error) {
				row, err := rule.WithContext(ctx).
					Clauses(clause.Locking{Strength: "UPDATE"}).
					Where(rule.ID.Eq(id)).
					Where(ownerConditions(ctx, rule.TableName())...).
					First()
				if isNoRows(err) {
					return nil, nil
				}
				return row, err
			}
			for i, input := range normalized {
				if err := stateRuleAssetAvailable(ctx, tx, input, fmt.Sprintf("第 %d 条规则: ", i+1)); err != nil {
					return err
				}
				existing, err := owned(input.ID)
				if err != nil {
					return err
				}
				if existing == nil {
					taken, err := rule.WithContext(ctx).Where(rule.ID.Eq(input.ID)).Count()
					if err != nil {
						return err
					}
					if taken > 0 {
						input.ID = importedStateRuleID(ipc.OwnerID(ctx), input.ID)
						if existing, err = owned(input.ID); err != nil {
							return err
						}
						if existing == nil {
							// The derived ID is taken by yet another owner.
							if taken, err := rule.WithContext(ctx).Where(rule.ID.Eq(input.ID)).Count(); err != nil {
								return err
							} else if taken > 0 {
								input.ID = ids.New()
							}
						}
					}
				}
				switch {
				case existing == nil:
					row := stateRuleRow(ctx, input, now, input.UpdatedAt)
					row.Position = position
					position++
					creates = append(creates, &row)
					changes = append(changes, input)
					continue
				case input.UpdatedAt <= existing.UpdatedAt:
					result.Unchanged++
					continue
				}
				changes = append(changes, input)
				if _, err := rule.WithContext(ctx).
					Where(rule.ID.Eq(input.ID), rule.OwnerID.Eq(existing.OwnerID)).
					UpdateColumns(stateRuleAssignments(input, input.UpdatedAt)); err != nil {
					return err
				}
				result.Updated++
			}
			if len(changes) == 0 {
				return nil
			}
			// The updates above already landed in this transaction; the check
			// overlays the same values, and a rejection rolls everything back.
			if err := checkStateRuleLimits(ctx, tx, changes); err != nil {
				return err
			}
			if len(creates) == 0 {
				return nil
			}
			if err := rule.WithContext(ctx).Create(creates...); err != nil {
				return err
			}
			result.Created = len(creates)
			return nil
		})
	})
	if err != nil {
		return StateRuleImportResult{}, dbError(err)
	}
	return result, nil
}

// StateRulesFor returns the enabled rules that apply to sessions created
// through a host, in the order a helper tries them: the host's own rules
// before global ones, each in position order. Rules that no longer validate
// are returned in skipped by ID.
func (s *Store) StateRulesFor(ctx context.Context, assetID string) (rules []staterule.Rule, skipped []string, err error) {
	rows, err := s.StateRuleList(ctx)
	if err != nil {
		return nil, nil, err
	}
	var host, global []staterule.Rule
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		if row.Scope == StateRuleScopeHost && (assetID == "" || row.AssetID != assetID) {
			continue
		}
		rule := staterule.Normalize(StateRuleFromRow(row))
		if _, err := staterule.Validate(rule); err != nil {
			skipped = append(skipped, row.ID)
			continue
		}
		if row.Scope == StateRuleScopeHost {
			host = append(host, rule)
		} else {
			global = append(global, rule)
		}
	}
	return append(host, global...), skipped, nil
}

// StateRuleFingerprint summarizes the owner's rules so a cache can tell
// whether they changed without reading them: any create, edit, import or
// delete changes it.
type StateRuleFingerprint struct {
	Count   int64
	Latest  int64
	Updates int64
}

func (s *Store) StateRuleFingerprint(ctx context.Context) (StateRuleFingerprint, error) {
	rule := query.Use(s.db).StateRuleRow
	var result struct {
		Count   int64
		Latest  sql.NullInt64
		Updates sql.NullInt64
	}
	err := rule.WithContext(ctx).
		Select(rule.ID.Count().As("count"), rule.UpdatedAt.Max().As("latest"), rule.UpdatedAt.Sum().As("updates")).
		Where(ownerConditions(ctx, rule.TableName())...).
		Scan(&result)
	if err != nil {
		return StateRuleFingerprint{}, dbError(err)
	}
	return StateRuleFingerprint{Count: result.Count, Latest: result.Latest.Int64, Updates: result.Updates.Int64}, nil
}
