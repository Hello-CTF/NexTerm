package store

import (
	"context"
	"net"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ForwardRuleRow = model.ForwardRuleRow

type ForwardRuleInput struct {
	ID         string `json:"id,omitempty"`
	AssetID    string `json:"assetId"`
	Name       string `json:"name"`
	ListenPort int32  `json:"listenPort"`
	TargetHost string `json:"targetHost"`
	TargetPort int32  `json:"targetPort"`
	// AutoStart stays out of the IPC JSON surface until the forward contract
	// regeneration lands; startup recovery already persists and honors it.
	AutoStart bool `json:"-"`
}

func normalizeForwardRuleInput(input ForwardRuleInput) (ForwardRuleInput, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.AssetID = strings.TrimSpace(input.AssetID)
	input.Name = strings.TrimSpace(input.Name)
	input.TargetHost = strings.TrimSpace(input.TargetHost)
	input.TargetHost = strings.TrimPrefix(strings.TrimSuffix(input.TargetHost, "]"), "[")
	if input.AssetID == "" {
		return ForwardRuleInput{}, badParam(errString("转发资产不能为空"))
	}
	if input.TargetHost == "" {
		return ForwardRuleInput{}, badParam(errString("目标主机不能为空"))
	}
	if input.ListenPort < 1 || input.ListenPort > 65535 {
		return ForwardRuleInput{}, badParam(errString("监听端口必须在 1-65535 之间"))
	}
	if input.TargetPort < 1 || input.TargetPort > 65535 {
		return ForwardRuleInput{}, badParam(errString("目标端口必须在 1-65535 之间"))
	}
	if input.Name == "" {
		input.Name = net.JoinHostPort(input.TargetHost, strconv.Itoa(int(input.TargetPort)))
	}
	return input, nil
}

func forwardRuleAssetAvailable(ctx context.Context, db *gorm.DB, assetID string) error {
	asset := query.Use(db).AssetRow
	row, err := asset.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(asset.ID.Eq(assetID)).
		First()
	if isNoRows(err) {
		return badParam(errString("转发资产不存在"))
	}
	if err != nil {
		return dbError(err)
	}
	if row.OwnerID != ipc.OwnerID(ctx) {
		return ipc.NewError(ipc.CodeForbidden, "转发资产不属于当前用户")
	}
	if row.DeletedAt != nil {
		return badParam(errString("转发资产已删除，不能保存或启动规则"))
	}
	return nil
}

func (s *Store) ForwardRuleAssetAvailable(ctx context.Context, assetID string) error {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return badParam(errString("转发资产不能为空"))
	}
	return forwardRuleAssetAvailable(ctx, s.db, assetID)
}

// ForwardRuleAssetEnvironment reports the canonical environment of the asset a
// rule points at; startup recovery uses it to skip production assets.
func (s *Store) ForwardRuleAssetEnvironment(ctx context.Context, assetID string) (string, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return "", badParam(errString("转发资产不能为空"))
	}
	asset := query.Use(s.db).AssetRow
	row, err := asset.WithContext(ctx).
		Where(asset.ID.Eq(assetID)).
		First()
	if isNoRows(err) {
		return "", badParam(errString("转发资产不存在"))
	}
	if err != nil {
		return "", dbError(err)
	}
	if row.OwnerID != ipc.OwnerID(ctx) {
		return "", ipc.NewError(ipc.CodeForbidden, "转发资产不属于当前用户")
	}
	if row.DeletedAt != nil {
		return "", badParam(errString("转发资产已删除，不能保存或启动规则"))
	}
	return AssetEnvironment(*row), nil
}

func (s *Store) ForwardRuleList(ctx context.Context) ([]ForwardRuleRow, error) {
	rule := query.Use(s.db).ForwardRuleRow
	rows := []ForwardRuleRow{}
	err := rule.WithContext(ctx).
		Where(rule.OwnerID.Eq(ipc.OwnerID(ctx))).
		Order(rule.CreatedAt, rule.ID).
		Scan(&rows)
	if err != nil {
		return nil, dbError(err)
	}
	return rows, nil
}

func (s *Store) ForwardRuleGet(ctx context.Context, id string) (ForwardRuleRow, error) {
	rule := query.Use(s.db).ForwardRuleRow
	row, err := rule.WithContext(ctx).
		Where(rule.ID.Eq(strings.TrimSpace(id)), rule.OwnerID.Eq(ipc.OwnerID(ctx))).
		First()
	if isNoRows(err) {
		return ForwardRuleRow{}, notFound("转发规则")
	}
	if err != nil {
		return ForwardRuleRow{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) ForwardRuleSave(ctx context.Context, input ForwardRuleInput) (ForwardRuleRow, error) {
	normalized, err := normalizeForwardRuleInput(input)
	if err != nil {
		return ForwardRuleRow{}, err
	}
	if normalized.ID != "" {
		if err := EnsureID(normalized.ID); err != nil {
			return ForwardRuleRow{}, err
		}
		return s.forwardRuleUpdate(ctx, normalized)
	}
	return s.forwardRuleCreate(ctx, normalized)
}

func (s *Store) forwardRuleCreate(ctx context.Context, input ForwardRuleInput) (ForwardRuleRow, error) {
	now := ids.NowMS()
	row := ForwardRuleRow{
		ID: ids.New(), OwnerID: ipc.OwnerID(ctx), AssetID: input.AssetID, Name: input.Name,
		ListenPort: input.ListenPort, TargetHost: input.TargetHost, TargetPort: input.TargetPort,
		AutoStart: input.AutoStart,
		CreatedAt: now, UpdatedAt: now,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := forwardRuleAssetAvailable(ctx, tx, input.AssetID); err != nil {
			return err
		}
		return query.Use(tx).ForwardRuleRow.WithContext(ctx).Create(&row)
	})
	if err != nil {
		return ForwardRuleRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) forwardRuleUpdate(ctx context.Context, input ForwardRuleInput) (ForwardRuleRow, error) {
	var row ForwardRuleRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rule := query.Use(tx).ForwardRuleRow
		existing, err := rule.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(rule.ID.Eq(input.ID), rule.OwnerID.Eq(ipc.OwnerID(ctx))).
			First()
		if isNoRows(err) {
			return notFound("转发规则")
		}
		if err != nil {
			return err
		}
		if err := forwardRuleAssetAvailable(ctx, tx, input.AssetID); err != nil {
			return err
		}
		// AutoStart stays out of the IPC JSON surface until the forward contract
		// regeneration lands, so updates must keep the persisted flag; explicit
		// toggles go through ForwardRuleSetAutoStart.
		if _, err := rule.WithContext(ctx).
			Where(rule.ID.Eq(input.ID), rule.OwnerID.Eq(ipc.OwnerID(ctx))).
			UpdateColumns(map[string]any{
				"asset_id":    input.AssetID,
				"name":        input.Name,
				"listen_port": input.ListenPort,
				"target_host": input.TargetHost,
				"target_port": input.TargetPort,
				"auto_start":  existing.AutoStart,
				"updated_at":  ids.NowMS(),
			}); err != nil {
			return err
		}
		updated, err := rule.WithContext(ctx).
			Where(rule.ID.Eq(input.ID), rule.OwnerID.Eq(ipc.OwnerID(ctx))).
			First()
		if err != nil {
			return err
		}
		row = *updated
		return nil
	})
	if err != nil {
		return ForwardRuleRow{}, dbError(err)
	}
	return row, nil
}

// ForwardRuleSetAutoStart explicitly toggles a rule's persisted autoStart flag
// until the IPC surface can express field presence.
func (s *Store) ForwardRuleSetAutoStart(ctx context.Context, id string, autoStart bool) (ForwardRuleRow, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ForwardRuleRow{}, badParam(errString("转发规则 ID 不能为空"))
	}
	if err := EnsureID(id); err != nil {
		return ForwardRuleRow{}, err
	}
	var row ForwardRuleRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rule := query.Use(tx).ForwardRuleRow
		if _, err := rule.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(rule.ID.Eq(id), rule.OwnerID.Eq(ipc.OwnerID(ctx))).
			First(); isNoRows(err) {
			return notFound("转发规则")
		} else if err != nil {
			return err
		}
		if _, err := rule.WithContext(ctx).
			Where(rule.ID.Eq(id), rule.OwnerID.Eq(ipc.OwnerID(ctx))).
			UpdateColumns(map[string]any{
				"auto_start": autoStart,
				"updated_at": ids.NowMS(),
			}); err != nil {
			return err
		}
		updated, err := rule.WithContext(ctx).
			Where(rule.ID.Eq(id), rule.OwnerID.Eq(ipc.OwnerID(ctx))).
			First()
		if err != nil {
			return err
		}
		row = *updated
		return nil
	})
	if err != nil {
		return ForwardRuleRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) ForwardRuleDelete(ctx context.Context, id string) error {
	rule := query.Use(s.db).ForwardRuleRow
	info, err := rule.WithContext(ctx).
		Where(rule.ID.Eq(strings.TrimSpace(id)), rule.OwnerID.Eq(ipc.OwnerID(ctx))).
		Delete()
	if err != nil {
		return dbError(err)
	}
	if info.RowsAffected == 0 {
		return notFound("转发规则")
	}
	return nil
}
