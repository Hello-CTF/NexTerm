package sync

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// 链接登录的三态结果: connected 会话可用; mfa_required 待提交 TOTP 码;
// reset_required 待提交新密码完成强制改密。
const (
	LinkConnectConnected     = "connected"
	LinkConnectMFARequired   = "mfa_required"
	LinkConnectResetRequired = "reset_required"
)

type LinkConnectResult struct {
	State        string
	MFATicket    string
	MFAExpiresAt int64
	RecoveryKey  string
}

// 需要人工重新交互登录的两种如实状态: 自动探测(启动探测与 401 重登)无法完成,
// 记入 LastError 由设置页引导用户走 LinkSet 的分步入职。
var (
	errReloginMFA   = ipc.NewError(ipc.CodeForbidden, "账号已开启两步验证：请先在同步设置中完成验证")
	errReloginReset = ipc.NewError(ipc.CodeForbidden, "账号密码已被管理员重置：请先在同步设置中完成密码重置")
)

// pendingLinkLogin 是密码已通过、待第二因子或强制改密的中间态: 客户端已按
// 服务端合同持有票据(MFA)或受限会话 cookie+CSRF(reset_required)。
type pendingLinkLogin struct {
	client *remoteClient
	ticket string
}

func remoteConfigKey(config RemoteConfig) string {
	return config.URL + "\x00" + config.Username
}

// Connect 用保存的口令同步探测登录: 成功即建立并缓存会话; MFA/强制改密则
// 挂起中间态, 由 CompleteMFA / CompleteResetPassword 完成入职。
func (e *Engine) Connect(ctx context.Context, config RemoteConfig) (LinkConnectResult, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	key := remoteConfigKey(config)
	client, err := newRemoteClient(config)
	if err != nil {
		return LinkConnectResult{}, err
	}
	outcome, err := client.login(ctx, config.Username, config.Password)
	if err != nil {
		return LinkConnectResult{}, err
	}
	if outcome.mfaTicket != "" {
		e.sessionMu.Lock()
		e.pending[key] = &pendingLinkLogin{client: client, ticket: outcome.mfaTicket}
		e.sessionMu.Unlock()
		return LinkConnectResult{State: LinkConnectMFARequired, MFATicket: outcome.mfaTicket, MFAExpiresAt: outcome.mfaExpiresAt}, nil
	}
	if outcome.resetRequired {
		e.sessionMu.Lock()
		e.pending[key] = &pendingLinkLogin{client: client}
		e.sessionMu.Unlock()
		return LinkConnectResult{State: LinkConnectResetRequired}, nil
	}
	if _, err := e.establishSession(ctx, config, client, config.Password); err != nil {
		return LinkConnectResult{}, err
	}
	return LinkConnectResult{State: LinkConnectConnected}, nil
}

func (e *Engine) takePending(key string) *pendingLinkLogin {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	return e.pending[key]
}

func (e *Engine) clearPending(key string) {
	e.sessionMu.Lock()
	defer e.sessionMu.Unlock()
	delete(e.pending, key)
}

// CompleteMFA 提交 TOTP 码完成第二步入职; 口令未变, DEK 仍用保存的口令解包。
func (e *Engine) CompleteMFA(ctx context.Context, config RemoteConfig, code string) (LinkConnectResult, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	key := remoteConfigKey(config)
	pending := e.takePending(key)
	if pending == nil || pending.ticket == "" {
		return LinkConnectResult{}, ipc.NewError(ipc.CodeBadParam, "登录状态已过期，请重新连接")
	}
	if err := pending.client.loginTOTP(ctx, pending.ticket, code); err != nil {
		return LinkConnectResult{}, err
	}
	if _, err := e.establishSession(ctx, config, pending.client, config.Password); err != nil {
		return LinkConnectResult{}, err
	}
	e.clearPending(key)
	return LinkConnectResult{State: LinkConnectConnected}, nil
}

// CompleteResetPassword 提交新密码完成强制改密入职: 管理员重置已删除服务端
// DEK 信封, 这里用新口令签发全新 DEK 信封上传(与 WEB completeResetRequired 同语义,
// 旧密文不可恢复, 靠本地副本后续重新同步)。当前受限会话被服务端保留, 改密后即正式会话。
func (e *Engine) CompleteResetPassword(ctx context.Context, config RemoteConfig, newPassword string) (LinkConnectResult, error) {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	key := remoteConfigKey(config)
	pending := e.takePending(key)
	if pending == nil {
		return LinkConnectResult{}, ipc.NewError(ipc.CodeBadParam, "登录状态已过期，请重新连接")
	}
	dek, envelopes, recoveryKey, err := vault.GenerateUserDEKEnvelopes(newPassword)
	if err != nil {
		return LinkConnectResult{}, err
	}
	defer clear(dek)
	if err := pending.client.changeMyPassword(ctx, config.Password, newPassword, envelopes); err != nil {
		if errors.Is(err, errSessionExpired) {
			return LinkConnectResult{}, ipc.NewError(ipc.CodeForbidden, "登录状态已过期，请重新连接")
		}
		return LinkConnectResult{}, err
	}
	if _, err := e.establishSession(ctx, config, pending.client, newPassword); err != nil {
		return LinkConnectResult{}, err
	}
	e.clearPending(key)
	return LinkConnectResult{State: LinkConnectConnected, RecoveryKey: recoveryKey}, nil
}

// remoteCall 是类型化远端方法的会话供应与 401 重登环: 语义与 Sync 一致
// (ensureSession 缓存复用, 401 dropSession 后用保存的口令重登), 并以 opMu
// 与同步轮次互斥, 避免 dropSession 清空 DEK 时 syncOnce 仍在使用。
func (e *Engine) remoteCall(ctx context.Context, config RemoteConfig, fn func(ctx context.Context, client *remoteClient) error) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	for attempt := 0; attempt < maxSyncAttempts; attempt++ {
		session, err := e.ensureSession(ctx, config)
		if err != nil {
			return err
		}
		err = fn(ctx, session.client)
		if errors.Is(err, errSessionExpired) {
			e.dropSession(session.key)
			continue
		}
		return err
	}
	return ipc.NewError(ipc.CodeDisconnected, "远端会话反复失效，请重新登录")
}
