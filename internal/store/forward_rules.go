package store

import (
	"context"
	"net"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
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
	var asset AssetRow
	err := db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", assetID).
		First(&asset).Error
	if isNoRows(err) {
		return badParam(errString("转发资产不存在"))
	}
	if err != nil {
		return dbError(err)
	}
	if asset.OwnerID != ipc.OwnerID(ctx) {
		return ipc.NewError(ipc.CodeForbidden, "转发资产不属于当前用户")
	}
	if asset.DeletedAt != nil {
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

func (s *Store) ForwardRuleList(ctx context.Context) ([]ForwardRuleRow, error) {
	rows := []ForwardRuleRow{}
	err := s.db.WithContext(ctx).
		Where("owner_id = ?", ipc.OwnerID(ctx)).
		Order("created_at").Order("id").
		Find(&rows).Error
	if err != nil {
		return nil, dbError(err)
	}
	return rows, nil
}

func (s *Store) ForwardRuleGet(ctx context.Context, id string) (ForwardRuleRow, error) {
	var row ForwardRuleRow
	err := s.db.WithContext(ctx).
		Where("id = ? AND owner_id = ?", strings.TrimSpace(id), ipc.OwnerID(ctx)).
		First(&row).Error
	if isNoRows(err) {
		return ForwardRuleRow{}, notFound("转发规则")
	}
	if err != nil {
		return ForwardRuleRow{}, dbError(err)
	}
	return row, nil
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
		CreatedAt: now, UpdatedAt: now,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := forwardRuleAssetAvailable(ctx, tx, input.AssetID); err != nil {
			return err
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return ForwardRuleRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) forwardRuleUpdate(ctx context.Context, input ForwardRuleInput) (ForwardRuleRow, error) {
	var row ForwardRuleRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_id = ?", input.ID, ipc.OwnerID(ctx)).
			First(&row).Error
		if isNoRows(err) {
			return notFound("转发规则")
		}
		if err != nil {
			return err
		}
		if err := forwardRuleAssetAvailable(ctx, tx, input.AssetID); err != nil {
			return err
		}
		values := map[string]any{
			"asset_id": input.AssetID, "name": input.Name, "listen_port": input.ListenPort,
			"target_host": input.TargetHost, "target_port": input.TargetPort, "updated_at": ids.NowMS(),
		}
		result := tx.Model(&ForwardRuleRow{}).
			Where("id = ? AND owner_id = ?", input.ID, ipc.OwnerID(ctx)).
			Updates(values)
		if result.Error != nil {
			return result.Error
		}
		return tx.Where("id = ? AND owner_id = ?", input.ID, ipc.OwnerID(ctx)).First(&row).Error
	})
	if err != nil {
		return ForwardRuleRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) ForwardRuleDelete(ctx context.Context, id string) error {
	result := s.db.WithContext(ctx).
		Where("id = ? AND owner_id = ?", strings.TrimSpace(id), ipc.OwnerID(ctx)).
		Delete(&ForwardRuleRow{})
	if result.Error != nil {
		return dbError(result.Error)
	}
	if result.RowsAffected == 0 {
		return notFound("转发规则")
	}
	return nil
}
