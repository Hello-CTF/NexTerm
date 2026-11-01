package remotedesktop

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type credentialDecryptor interface {
	DecryptCredentialString(context.Context, store.CredentialRow) (string, error)
}

// StoreResolver reads rdp and vnc assets from the application store.
type StoreResolver struct {
	store     *store.Store
	decryptor credentialDecryptor
}

func NewStoreResolver(database *store.Store, decryptor credentialDecryptor) *StoreResolver {
	return &StoreResolver{store: database, decryptor: decryptor}
}

func (r *StoreResolver) ResolveAsset(ctx context.Context, id string) (Asset, error) {
	if r == nil || r.store == nil {
		return Asset{}, ipc.NewError(ipc.CodeUnsupported, "资产存储未配置")
	}
	row, err := r.store.AssetGet(ctx, id)
	if err != nil {
		return Asset{}, err
	}
	if row.DeletedAt != nil {
		return Asset{}, ipc.NewError(ipc.CodeNotFound, "资产已删除")
	}
	asset := Asset{ID: row.ID, OwnerID: row.OwnerID, Kind: row.Kind, Name: row.Name, OptionsJSON: row.OptionsJSON,
		HasPassword: row.CredID != nil && *row.CredID != ""}
	if row.Host != nil {
		asset.Host = *row.Host
	}
	if row.Port != nil {
		asset.Port = int(*row.Port)
	}
	if row.Username != nil {
		asset.Username = *row.Username
	}
	return asset, nil
}

func (r *StoreResolver) ResolvePassword(ctx context.Context, id string) (string, error) {
	if r == nil || r.store == nil {
		return "", ipc.NewError(ipc.CodeUnsupported, "资产存储未配置")
	}
	row, err := r.store.AssetGet(ctx, id)
	if err != nil {
		return "", err
	}
	if row.CredID == nil || *row.CredID == "" {
		return "", nil
	}
	if r.decryptor == nil {
		return "", ipc.NewError(ipc.CodeUnsupported, "凭据解密器未配置")
	}
	credential, err := r.store.CredentialGetRow(ctx, *row.CredID)
	if err != nil {
		return "", err
	}
	return r.decryptor.DecryptCredentialString(ctx, credential)
}

// StoreCertificates pins fingerprints in the SSH known-hosts ledger under the rdp-tls / vnc-tls key types.
// Rows carry no key material, so they never reach the OpenSSH mirror, and they sync like any known host.
type StoreCertificates struct{ store *store.Store }

func NewStoreCertificates(database *store.Store) *StoreCertificates {
	return &StoreCertificates{store: database}
}

func (c *StoreCertificates) Get(ctx context.Context, host string, port int, keyType string) (string, bool, error) {
	if c == nil || c.store == nil {
		return "", false, errors.New("known host store is not configured")
	}
	row, found, err := c.store.KnownHostGet(ctx, host, int32(port), keyType)
	return row.Fingerprint, found, err
}

func (c *StoreCertificates) Accept(ctx context.Context, host string, port int, keyType, fingerprint string) error {
	return c.store.KnownHostAccept(ctx, host, int32(port), keyType, fingerprint)
}

// StoreAuditor writes to the application audit log.
type StoreAuditor struct{ store *store.Store }

func NewStoreAuditor(database *store.Store) *StoreAuditor { return &StoreAuditor{store: database} }

func (a *StoreAuditor) Audit(ctx context.Context, event AuditEvent) error {
	input := store.AuditInput{Source: "remotedesktop", Kind: event.Kind, Payload: event.Payload}
	if event.AssetID != "" {
		id := event.AssetID
		input.AssetID = &id
	}
	return a.store.AuditInsert(ctx, input)
}
