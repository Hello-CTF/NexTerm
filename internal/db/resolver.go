package db

import (
	"context"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type CredentialDecryptor interface {
	DecryptCredentialString(context.Context, store.CredentialRow) (string, error)
}

type StoreAssetResolver struct {
	store     *store.Store
	decryptor CredentialDecryptor
}

func NewStoreAssetResolver(database *store.Store, decryptor CredentialDecryptor) *StoreAssetResolver {
	return &StoreAssetResolver{store: database, decryptor: decryptor}
}

func (r *StoreAssetResolver) ResolveDBAsset(ctx context.Context, id string) (Asset, error) {
	if r == nil || r.store == nil {
		return Asset{}, internalError("读取数据库资产", errors.New("资产存储未配置"))
	}
	row, err := r.store.AssetGet(ctx, id)
	if err != nil {
		return Asset{}, err
	}
	asset := Asset{
		Kind:        row.Kind,
		Host:        row.Host,
		Username:    row.Username,
		OptionsJSON: row.OptionsJSON,
	}
	if row.Port != nil {
		port := int(*row.Port)
		asset.Port = &port
	}
	if row.CredID != nil && *row.CredID != "" {
		if r.decryptor == nil {
			return Asset{}, internalError("读取数据库凭据", errors.New("凭据解密器未配置"))
		}
		credential, err := r.store.CredentialGetRow(ctx, *row.CredID)
		if err != nil {
			return Asset{}, err
		}
		asset.Password, err = r.decryptor.DecryptCredentialString(ctx, credential)
		if err != nil {
			return Asset{}, err
		}
	}
	return asset, nil
}
