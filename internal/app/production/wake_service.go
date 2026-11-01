package production

import (
	"context"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/wol"
)

// newProductionWakeService wires Wake-on-LAN to the stored assets, the audit log and the connected device sessions.
func newProductionWakeService(database *store.Store, sessions dbTunnelSessionManager) *wol.Service {
	device := newSessionAssetDialer(database, sessions, true)
	return wol.NewService(
		wakeStoreResolver{store: database},
		wol.WithAuditor(wakeStoreAuditor{store: database}),
		wol.WithDeviceDialer(func(ctx context.Context, viaAssetID string) (wol.DialFunc, error) {
			dial, err := device(ctx, viaAssetID)
			if err != nil {
				return nil, err
			}
			return func(ctx context.Context, network, address string) (net.Conn, error) {
				return dial(ctx, network, address)
			}, nil
		}),
	)
}

type wakeStoreResolver struct{ store *store.Store }

func (r wakeStoreResolver) ResolveAsset(ctx context.Context, id string) (wol.Asset, error) {
	row, err := r.store.AssetGet(ctx, id)
	if err != nil {
		return wol.Asset{}, err
	}
	if row.DeletedAt != nil {
		return wol.Asset{}, ipc.NewError(ipc.CodeNotFound, "资产已删除")
	}
	return wol.Asset{ID: row.ID, OwnerID: row.OwnerID, Kind: row.Kind, Name: row.Name, OptionsJSON: row.OptionsJSON}, nil
}

type wakeStoreAuditor struct{ store *store.Store }

func (a wakeStoreAuditor) Audit(ctx context.Context, event wol.AuditEvent) error {
	input := store.AuditInput{Source: "wol", Kind: event.Kind, Payload: event.Payload}
	if event.AssetID != "" {
		id := event.AssetID
		input.AssetID = &id
	}
	return a.store.AuditInsert(ctx, input)
}
