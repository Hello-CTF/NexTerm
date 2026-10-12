package sync

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// RemoteProxy 是桌面端经已登录服务端账号调远程 API 的代理面: 会话由
// engine.ensureSession 供应(401 自动重登), 方法与服务端路由一一对应的
// 类型化合同, 不做通用透传。仅桌面端可用。
type RemoteProxy struct {
	service *Service
}

func (s *Service) Remote() *RemoteProxy {
	return &RemoteProxy{service: s}
}

type RemoteOK struct {
	OK bool `json:"ok"`
}

type RemoteAutostartResult struct {
	OK               bool `json:"ok"`
	DesiredAutostart bool `json:"desired_autostart"`
}

type RemotePasswordChangeResult struct {
	OK          bool   `json:"ok"`
	RecoveryKey string `json:"recoveryKey,omitempty"`
}

func (s *Service) remoteConfig(ctx context.Context) (RemoteConfig, error) {
	if err := s.requireDesktop(); err != nil {
		return RemoteConfig{}, err
	}
	secret, err := s.linkSecret(ctx)
	if err != nil {
		return RemoteConfig{}, err
	}
	if secret.URL == "" || secret.Username == "" || secret.Password == "" {
		return RemoteConfig{}, ipc.NewError(ipc.CodeBadParam, "同步链接需要同时配置 URL、用户名和口令")
	}
	return remoteConfigFromSecret(secret), nil
}

func (p *RemoteProxy) call(ctx context.Context, fn func(ctx context.Context, client *remoteClient) error) error {
	config, err := p.service.remoteConfig(ctx)
	if err != nil {
		return err
	}
	return p.service.engine.remoteCall(ctx, config, fn)
}

func remoteGet[T any](ctx context.Context, p *RemoteProxy, fetch func(context.Context, *remoteClient) (T, error)) (T, error) {
	var result T
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		value, err := fetch(ctx, client)
		if err != nil {
			return err
		}
		result = value
		return nil
	})
	return result, err
}

// fleet。

func (p *RemoteProxy) ListDevices(ctx context.Context) (*RemoteFleetDevices, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteFleetDevices, error) {
		return client.ListDevices(ctx)
	})
}

func (p *RemoteProxy) DeviceMetrics(ctx context.Context, deviceID string, sinceMS int64) (*RemoteMetricsSamples, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteMetricsSamples, error) {
		return client.DeviceMetrics(ctx, deviceID, sinceMS)
	})
}

func (p *RemoteProxy) RevokeDevice(ctx context.Context, deviceID string) (RemoteOK, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.RevokeDevice(ctx, deviceID)
	})
	return RemoteOK{OK: err == nil}, err
}

func (p *RemoteProxy) ApproveDevice(ctx context.Context, deviceID string) (RemoteOK, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.ApproveDevice(ctx, deviceID)
	})
	return RemoteOK{OK: err == nil}, err
}

func (p *RemoteProxy) RejectDevice(ctx context.Context, deviceID string) (RemoteOK, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.RejectDevice(ctx, deviceID)
	})
	return RemoteOK{OK: err == nil}, err
}

func (p *RemoteProxy) SetDeviceAutostart(ctx context.Context, deviceID string, desired bool) (RemoteAutostartResult, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.SetDeviceAutostart(ctx, deviceID, desired)
	})
	return RemoteAutostartResult{OK: err == nil, DesiredAutostart: desired}, err
}

func (p *RemoteProxy) CreateEnrollCode(ctx context.Context, ttlMS int64, userID string) (*RemoteEnrollCode, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteEnrollCode, error) {
		return client.CreateEnrollCode(ctx, ttlMS, userID)
	})
}

func (p *RemoteProxy) GetBaseURLs(ctx context.Context) (*RemoteBaseURLs, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteBaseURLs, error) {
		return client.GetBaseURLs(ctx)
	})
}

func (p *RemoteProxy) PutBaseURLs(ctx context.Context, entries []RemoteBaseURLEntry) (*RemoteBaseURLs, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteBaseURLs, error) {
		return client.PutBaseURLs(ctx, entries)
	})
}

// sharing。

func (p *RemoteProxy) ListHostShares(ctx context.Context) (*RemoteHostShares, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteHostShares, error) {
		return client.ListHostShares(ctx)
	})
}

func (p *RemoteProxy) CreateHostShare(ctx context.Context, input RemoteHostShareInput) (*RemoteHostShare, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteHostShare, error) {
		return client.CreateHostShare(ctx, input)
	})
}

func (p *RemoteProxy) RevokeHostShare(ctx context.Context, id string) (RemoteOK, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.RevokeHostShare(ctx, id)
	})
	return RemoteOK{OK: err == nil}, err
}

func (p *RemoteProxy) ListShareLinks(ctx context.Context) (*RemoteShareLinks, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteShareLinks, error) {
		return client.ListShareLinks(ctx)
	})
}

func (p *RemoteProxy) CreateShareLink(ctx context.Context, input RemoteShareLinkInput) (*RemoteShareLink, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteShareLink, error) {
		return client.CreateShareLink(ctx, input)
	})
}

func (p *RemoteProxy) RevokeShareLink(ctx context.Context, id string) (RemoteOK, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.RevokeShareLink(ctx, id)
	})
	return RemoteOK{OK: err == nil}, err
}

func (p *RemoteProxy) ListPreviewLinks(ctx context.Context) (*RemotePreviewLinks, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemotePreviewLinks, error) {
		return client.ListPreviewLinks(ctx)
	})
}

func (p *RemoteProxy) RevokePreviewLink(ctx context.Context, id string) (RemoteOK, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.RevokePreviewLink(ctx, id)
	})
	return RemoteOK{OK: err == nil}, err
}

// 远程会话壳与预览创建: 桌面端只读预览中继的管理面。

func (p *RemoteProxy) RemoteSessionCreate(ctx context.Context, title, assetID string, cols, rows uint32) (*RemoteSession, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteSession, error) {
		return client.RemoteSessionCreate(ctx, title, assetID, cols, rows)
	})
}

func (p *RemoteProxy) RemoteSessionDelete(ctx context.Context, id string) (RemoteOK, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.RemoteSessionDelete(ctx, id)
	})
	return RemoteOK{OK: err == nil}, err
}

func (p *RemoteProxy) CreatePreviewLink(ctx context.Context, sessionID string, ttlMS int64) (*RemotePreviewLinkCreate, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemotePreviewLinkCreate, error) {
		return client.CreatePreviewLink(ctx, sessionID, ttlMS)
	})
}

// ServerBaseURL 返回账号连接的规范化服务端地址, 用于拼公开预览 URL;
// 只读本地链接配置, 不要求会话在线。
func (p *RemoteProxy) ServerBaseURL(ctx context.Context) (string, error) {
	config, err := p.service.remoteConfig(ctx)
	if err != nil {
		return "", err
	}
	return normalizeBase(config.URL)
}

// account。

func (p *RemoteProxy) Me(ctx context.Context) (*RemoteAccountMe, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteAccountMe, error) {
		return client.Me(ctx)
	})
}

func (p *RemoteProxy) ListMyDevices(ctx context.Context) (*RemoteAccountDevices, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteAccountDevices, error) {
		return client.ListMyDevices(ctx)
	})
}

func (p *RemoteProxy) RevokeMyDevice(ctx context.Context, deviceID string) (RemoteOK, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.RevokeMyDevice(ctx, deviceID)
	})
	return RemoteOK{OK: err == nil}, err
}

func (p *RemoteProxy) AdminListUsers(ctx context.Context) (*RemoteAccountUsers, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteAccountUsers, error) {
		return client.AdminListUsers(ctx)
	})
}

func (p *RemoteProxy) AdminCreateUser(ctx context.Context, username, password, displayName string) (*RemoteAccountUserResult, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (*RemoteAccountUserResult, error) {
		return client.AdminCreateUser(ctx, username, password, displayName)
	})
}

func (p *RemoteProxy) AdminSetUserState(ctx context.Context, userID, state string) (RemoteOK, error) {
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		return client.AdminSetUserState(ctx, userID, state)
	})
	return RemoteOK{OK: err == nil}, err
}

// ChangeMyPassword 主动改密: 用旧口令解出 DEK, 用新口令与新恢复密钥重包裹
// (密文不动, 与 WEB changePassword 同语义); 成功后同步更新本地保存的链接口令。
func (p *RemoteProxy) ChangeMyPassword(ctx context.Context, oldPassword, newPassword string) (*RemotePasswordChangeResult, error) {
	recoveryKey := ""
	err := p.call(ctx, func(ctx context.Context, client *remoteClient) error {
		envelopes, err := client.dekEnvelopes(ctx)
		if err != nil {
			return err
		}
		dek, err := vault.UnwrapUserDEK(oldPassword, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams)
		if err != nil {
			return err
		}
		defer clear(dek)
		envelope, salt, params, err := vault.WrapUserDEK(newPassword, dek)
		if err != nil {
			return err
		}
		canonical, err := vault.GenerateRecoveryKey()
		if err != nil {
			return err
		}
		recoveryEnvelope, err := vault.WrapUserDEKWithRecovery(canonical, dek)
		if err != nil {
			return err
		}
		updated := &vault.UserDEKEnvelopes{
			DEKEnvelope: envelope, KDFSalt: salt, KDFParams: params,
			RecoveryEnvelope: recoveryEnvelope, RecoveryHash: vault.RecoveryKeyHash(canonical),
		}
		if err := client.changeMyPassword(ctx, oldPassword, newPassword, updated); err != nil {
			return err
		}
		recoveryKey = vault.FormatRecoveryKey(canonical)
		return nil
	})
	if err != nil {
		return nil, err
	}
	p.service.updateLinkedPassword(ctx, oldPassword, newPassword)
	return &RemotePasswordChangeResult{OK: true, RecoveryKey: recoveryKey}, nil
}

// updateLinkedPassword 在远端改密成功后把本地保存的链接口令一并更新;
// 远端已生效, 本地更新失败只记日志(下次登录失败会自然暴露)。
func (s *Service) updateLinkedPassword(ctx context.Context, oldPassword, newPassword string) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	secret, err := s.linkSecret(ctx)
	if err != nil || secret.Password != oldPassword {
		return
	}
	secret.Password = newPassword
	if err := s.saveLinkSecret(ctx, secret); err != nil {
		s.engine.logger.Warn("linked password update failed", "error", err)
	}
}

// Healthz 读服务端版本; 空串表示版本不可用, 调用方必须隐藏依赖版本的 UI。
func (p *RemoteProxy) Healthz(ctx context.Context) (string, error) {
	return remoteGet(ctx, p, func(ctx context.Context, client *remoteClient) (string, error) {
		return client.Healthz(ctx)
	})
}
