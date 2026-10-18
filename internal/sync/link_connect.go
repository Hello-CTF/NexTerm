package sync

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// Link login tri-state results: connected means the session is usable; mfa_required awaits a TOTP code;
// reset_required awaits a new password to complete the forced change.
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

// The two truthful states that require manual interactive re-login: automatic probes (startup probe and 401 re-login) cannot complete them,
// so they are recorded in LastError and the settings page guides the user through LinkSet's step-by-step onboarding.
var (
	errReloginMFA   = ipc.NewError(ipc.CodeForbidden, "账号已开启两步验证：请先在同步设置中完成验证")
	errReloginReset = ipc.NewError(ipc.CodeForbidden, "账号密码已被管理员重置：请先在同步设置中完成密码重置")
)

// pendingLinkLogin is the intermediate state after the password has passed but a second factor or forced password change is pending: the client holds
// the ticket (MFA) or the restricted session cookie+CSRF (reset_required) per the server contract.
type pendingLinkLogin struct {
	client *remoteClient
	ticket string
}

func remoteConfigKey(config RemoteConfig) string {
	return config.URL + "\x00" + config.Username
}

// Connect probes login synchronously with the saved password: on success the session is established and cached; MFA/forced password change
// parks the intermediate state for CompleteMFA / CompleteResetPassword to finish onboarding.
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

// CompleteMFA submits a TOTP code to finish the second onboarding step; the password is unchanged, so the DEK is still unwrapped with the saved password.
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

// CompleteResetPassword submits a new password to finish forced-password-change onboarding: the admin reset deleted the server-side
// DEK envelope, so a brand-new DEK envelope is issued with the new password and uploaded here (same semantics as WEB completeResetRequired;
// old ciphertext is unrecoverable and relies on the local replica re-syncing later). The server keeps the current restricted session, which becomes a formal session after the change.
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
	connectedConfig := config
	connectedConfig.Password = newPassword
	if _, err := e.establishSession(ctx, connectedConfig, pending.client, newPassword); err != nil {
		return LinkConnectResult{}, err
	}
	e.clearPending(key)
	return LinkConnectResult{State: LinkConnectConnected, RecoveryKey: recoveryKey}, nil
}

// remoteCall is the session supply and 401 re-login loop for typed remote methods: same semantics as Sync
// (ensureSession cache reuse, 401 dropSession then re-login with the saved password), and it is mutually exclusive
// with sync rounds via opMu, so a dropSession clearing the DEK cannot race a running syncOnce.
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
