package production

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type dbTunnelAssetStore interface {
	AssetGet(context.Context, string) (store.AssetRow, error)
}

type dbTunnelSessionManager interface {
	ListSessionsFor(context.Context) ([]session.SessionInfo, error)
	TransportFor(context.Context, string) (base.Transport, error)
}

func newProductionDatabaseService(database *store.Store, decryptor db.CredentialDecryptor, sessions dbTunnelSessionManager) *db.Service {
	return db.NewService(
		db.NewStoreAssetResolver(database, decryptor),
		db.WithDialerFactory(newProductionDatabaseDialer(database, sessions)),
	)
}

func newProductionDatabaseDialer(database dbTunnelAssetStore, sessions dbTunnelSessionManager) db.DialerFactory {
	dialer := newSessionAssetDialer(database, sessions, false)
	return func(connectCtx context.Context, viaAssetID string) (db.DialContextFunc, error) {
		dial, err := dialer(connectCtx, viaAssetID)
		if err != nil {
			return nil, err
		}
		return db.DialContextFunc(dial), nil
	}
}

type sessionDialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// deviceAssetPrefix names fleet devices, which have no asset row: their session is the only identity.
const deviceAssetPrefix = "device/"

// newSessionAssetDialer returns a factory of dialers that open connections through the live, connected session of a
// jump asset. SSH assets are always allowed; allowDevice additionally accepts fleet devices ("device/<id>").
func newSessionAssetDialer(database dbTunnelAssetStore, sessions dbTunnelSessionManager, allowDevice bool) func(context.Context, string) (sessionDialFunc, error) {
	return func(connectCtx context.Context, viaAssetID string) (sessionDialFunc, error) {
		if database == nil || sessions == nil {
			return nil, ipc.NewError(ipc.CodeUnsupported, "数据库 SSH 隧道未配置")
		}
		viaAssetID = strings.TrimSpace(viaAssetID)
		if viaAssetID == "" {
			return nil, ipc.NewError(ipc.CodeBadParam, "SSH 跳板资产 ID 不能为空")
		}
		ownerID, authenticated := ipc.UserIDFromContext(connectCtx)
		openAccess := !authenticated
		wantKind, label := session.KindSSH, "SSH"
		isDevice := allowDevice && strings.HasPrefix(viaAssetID, deviceAssetPrefix)
		if isDevice {
			wantKind, label = session.KindDevice, "设备"
		}

		// Snapshot identity only. Each physical dial uses its own context and resolves the current session transport.
		return func(ctx context.Context, network, address string) (net.Conn, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			requestCtx := ctx
			if authenticated {
				requestCtx = ipc.WithUserID(ctx, ownerID)
			} else if openAccess {
				requestCtx = session.WithOpenAccess(ctx)
			}

			// A device has no asset row; the owner-scoped session list below is its ownership check.
			if !isDevice {
				asset, err := database.AssetGet(requestCtx, viaAssetID)
				if err != nil {
					return nil, fmt.Errorf("读取 SSH 跳板资产: %w", err)
				}
				if asset.OwnerID != ownerID {
					return nil, ipc.NewError(ipc.CodeForbidden, "无权使用该 SSH 跳板资产")
				}
				if asset.DeletedAt != nil {
					return nil, ipc.NewError(ipc.CodeNotFound, "SSH 跳板资产已删除")
				}
				if asset.Kind != session.KindSSH {
					return nil, ipc.NewError(ipc.CodeBadParam, "viaAssetId 必须引用 SSH 资产")
				}
			}

			infos, err := sessions.ListSessionsFor(requestCtx)
			if err != nil {
				return nil, session.IPCError(err)
			}
			for _, candidate := range infos {
				if candidate.AssetID != viaAssetID || candidate.Kind != wantKind || candidate.Status != session.StatusConnected {
					continue
				}
				transport, err := sessions.TransportFor(requestCtx, candidate.ID)
				if err != nil {
					return nil, session.IPCError(err)
				}
				if transport.Kind() != wantKind {
					return nil, ipc.NewError(ipc.CodeBadParam, "viaAssetId 的当前会话不是 "+label)
				}
				if !transport.IsAlive() {
					return nil, ipc.NewError(ipc.CodeDisconnected, label+" 跳板会话已断开，请重新连接")
				}
				dialer, ok := transport.(base.Dialer)
				if !ok {
					return nil, ipc.NewError(ipc.CodeUnsupported, "当前 "+label+" 会话不支持隧道拨号")
				}
				connection, err := dialer.DialContext(requestCtx, network, address)
				if err != nil {
					return nil, fmt.Errorf(label+" 隧道拨号失败: %w", err)
				}
				return connection, nil
			}
			return nil, ipc.NewError(ipc.CodeDisconnected, label+" 跳板资产没有已连接会话，请先连接该主机")
		}, nil
	}
}
