package production

import (
	"context"
	"sync/atomic"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

// productionFileChannels serves the files of SSH hosts through the helper
// file channel unless the host settings keep them on SFTP.
type productionFileChannels struct {
	database *store.Store
	resolver *sshdaemon.Resolver
	helpers  *productionHelperService
	// changed runs when a connection switched the channel serving its files.
	changed atomic.Pointer[func(base.Transport)]
}

func (p *productionFileChannels) attach(ctx context.Context, assetID string, client *ssh.Client) {
	if p == nil || p.resolver == nil {
		return
	}
	owner := ipc.OwnerID(ctx)
	client.SetFileChannel(ssh.FileChannelConfig{
		SFTPOnly: func(ctx context.Context) bool {
			if assetID == "" {
				return false
			}
			row, err := p.database.AssetGetLive(ownerContext(ctx, owner), assetID)
			return err == nil && store.AssetFileChannelSFTP(row.OptionsJSON)
		},
		Open: func(ctx context.Context) (ssh.FileChannelFS, error) {
			resolver := *p.resolver
			if p.helpers != nil {
				if err := p.helpers.configureResolver(&resolver, assetID); err != nil {
					return nil, err
				}
			}
			return resolver.OpenFileChannel(ownerContext(ctx, owner), client)
		},
		Changed: func() {
			if changed := p.changed.Load(); changed != nil {
				(*changed)(client)
			}
		},
	})
}
