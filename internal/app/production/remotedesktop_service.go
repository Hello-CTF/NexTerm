package production

import (
	"context"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/remotedesktop"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// newProductionRemoteDesktopService wires RDP/VNC relaying to the store, the vault and the live sessions that serve as
// SSH or fleet-device jump hosts.
func newProductionRemoteDesktopService(database *store.Store, decryptor db.CredentialDecryptor, sessions dbTunnelSessionManager) *remotedesktop.Service {
	jump := newSessionAssetDialer(database, sessions, true)
	return remotedesktop.NewService(
		remotedesktop.NewStoreResolver(database, decryptor),
		remotedesktop.WithCertificateStore(remotedesktop.NewStoreCertificates(database)),
		remotedesktop.WithAuditor(remotedesktop.NewStoreAuditor(database)),
		remotedesktop.WithDialerFactory(func(ctx context.Context, viaAssetID string) (remotedesktop.DialContextFunc, error) {
			dial, err := jump(ctx, viaAssetID)
			if err != nil {
				return nil, err
			}
			return func(ctx context.Context, network, address string) (net.Conn, error) {
				return dial(ctx, network, address)
			}, nil
		}),
	)
}
