package sync

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	stdsync "sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

const (
	settingLink                 = "sync.link"
	defaultBackgroundSyncPeriod = time.Minute
)

type Option func(*Service)

// WithMetadata 保留应用版本与桌面端标记; v2 协议不再向对端暴露版本与来源标识。
func WithMetadata(appVersion string, desktop bool) Option {
	return func(s *Service) {
		s.appVersion = appVersion
		s.desktop = desktop
	}
}

func WithGatewayAuthKey(key string) Option {
	return func(s *Service) {
		s.gatewayAuthKey = key
	}
}

func WithEvents(events ipc.Emitter) Option {
	return func(s *Service) {
		s.events = events
	}
}

func WithLogger(logger *slog.Logger) Option {
	return func(s *Service) {
		s.logger = logger
	}
}

func WithProfilesReload(reload func(context.Context) error) Option {
	return func(s *Service) {
		s.profilesReload = reload
	}
}

func WithPermissionsReload(reload func(context.Context) error) Option {
	return func(s *Service) {
		s.permissionsReload = reload
	}
}

type Service struct {
	store             *store.Store
	vault             *vault.Vault
	engine            *Engine
	appVersion        string
	desktop           bool
	gatewayAuthKey    string
	events            ipc.Emitter
	logger            *slog.Logger
	profilesReload    func(context.Context) error
	permissionsReload func(context.Context) error
	syncPeriod        time.Duration

	settingsMu  stdsync.Mutex
	lifecycleMu stdsync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	wakeCh      chan struct{}
	stopping    bool

	roundMu        stdsync.RWMutex
	roundStatus    syncRoundStatus
	blockedNoticed bool
}

// syncRoundStatus 是最近一轮 Service.Sync 的内存态三件套, 形状照 store.RetentionStatus。
type syncRoundStatus struct {
	LastAttemptAt time.Time `json:"lastAttemptAt"`
	LastSuccessAt time.Time `json:"lastSuccessAt"`
	LastError     string    `json:"lastError,omitempty"`
}

func (s *Service) GatewayAuthKey() string { return s.gatewayAuthKey }

func (s *Service) SetPermissionsReload(reload func(context.Context) error) {
	s.permissionsReload = reload
}

func New(db *store.Store, credentialVault *vault.Vault, options ...Option) *Service {
	s := &Service{store: db, vault: credentialVault, syncPeriod: defaultBackgroundSyncPeriod, wakeCh: make(chan struct{}, 1)}
	for _, option := range options {
		option(s)
	}
	s.engine = NewEngine(db, credentialVault, s.logger)
	if credentialVault != nil {
		// 凭据库解锁后立即唤醒一轮后台同步: 启动时锁定的库在解锁后不必等下一个周期。
		credentialVault.AddUnlockListener(s.wake)
	}
	return s
}

func (s *Service) Start(ctx context.Context) error {
	if !s.desktop {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping {
		return errors.New("同步服务正在关闭")
	}
	if s.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.syncPeriod)
		defer ticker.Stop()
		s.probeSavedLink(runCtx)
		for {
			s.syncBackground(runCtx)
			select {
			case <-runCtx.Done():
				return
			case <-s.wakeCh:
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (s *Service) syncBackground(ctx context.Context) {
	ctx = vault.WithBackground(ctx)
	link, err := s.LinkGet(ctx)
	if err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && (appErr.Code == ipc.CodeVaultLocked || appErr.Code == ipc.CodeVaultNotInit) {
			s.noteSyncBlocked(appErr)
			return
		}
		s.engine.logger.Warn("background sync link read failed", "error", err)
		return
	}
	s.clearSyncBlocked()
	if !link.IsConfigured() {
		return
	}
	wasLoggedIn := s.engine.hasSession()
	if _, err := s.Sync(ctx); err != nil && ctx.Err() == nil {
		s.engine.logger.Warn("background sync failed", "error", err)
	}
	// 后台轮新建立会话(解锁唤醒/故障恢复)时广播状态, 已挂载的设置页才能自动刷新为已登录。
	if !wasLoggedIn && s.engine.hasSession() {
		_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, struct{}{})
	}
}

// noteSyncBlocked 记录凭据库锁定/未初始化导致的后台同步暂停: 写入轮次状态使 Status 可见
// (此前静默跳过, 用户无从得知同步停摆), 且每段连续暂停期只记一行 Warn, 解锁恢复后
// roundStatus.LastError 由下一轮成功同步的 defer 清除。
func (s *Service) noteSyncBlocked(err error) {
	s.roundMu.Lock()
	s.roundStatus.LastAttemptAt = time.Now()
	s.roundStatus.LastError = err.Error()
	noticed := s.blockedNoticed
	s.blockedNoticed = true
	s.roundMu.Unlock()
	if !noticed {
		s.engine.logger.Warn("background sync paused: credential vault locked or not initialized", "error", err)
	}
}

// clearSyncBlocked 在链接可读(凭据库可用)后复位暂停闩锁: 下次进入暂停态会重新记日志。
func (s *Service) clearSyncBlocked() {
	s.roundMu.Lock()
	s.blockedNoticed = false
	s.roundMu.Unlock()
}

// probeSavedLink 启动时对已保存的链接做一次连接探测(与 LinkSet 的探测同路径):
// connected 即建立会话并刷新 VerifiedAt; mfa_required/reset_required 无法自动完成,
// 如实记入 LastError 引导重新交互登录; 探测失败只记 LastError, 不阻塞启动。
// 未配置或凭据库不可用(锁定/未初始化)时静默跳过, 由后台循环与解锁唤醒接手。
func (s *Service) probeSavedLink(ctx context.Context) {
	ctx = vault.WithBackground(ctx)
	link, err := s.LinkGet(ctx)
	if err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && (appErr.Code == ipc.CodeVaultLocked || appErr.Code == ipc.CodeVaultNotInit) {
			return
		}
		s.engine.logger.Warn("startup link probe read failed", "error", err)
		return
	}
	if !link.IsConfigured() {
		return
	}
	secret, err := s.linkSecret(ctx)
	if err != nil {
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	connect, err := s.engine.Connect(ctx, remoteConfigFromSecret(secret))
	if err != nil {
		s.recordProbe(ctx, err)
	} else {
		switch connect.State {
		case LinkConnectConnected:
			s.recordProbe(ctx, nil)
		case LinkConnectMFARequired:
			s.recordProbe(ctx, errReloginMFA)
		case LinkConnectResetRequired:
			s.recordProbe(ctx, errReloginReset)
		}
	}
	_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, struct{}{})
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.lifecycleMu.Lock()
	s.stopping = true
	cancel := s.cancel
	done := s.done
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.engine.opMu.Lock()
	defer s.engine.opMu.Unlock()
	s.engine.sessionMu.Lock()
	defer s.engine.sessionMu.Unlock()
	if s.engine.session != nil {
		clear(s.engine.session.dek)
		s.engine.session = nil
	}
	s.lifecycleMu.Lock()
	s.cancel = nil
	s.done = nil
	s.lifecycleMu.Unlock()
	return nil
}

// ObjectHandler 暴露服务端盲存储入口, 由服务端挂载到会话中间件之后。
func (s *Service) ObjectHandler() *ObjectHandler {
	return NewObjectHandler(s.store.GORM(), s.store.Backend(), s.engine.logger)
}

type Link struct {
	URL         string `json:"url"`
	Username    string `json:"username"`
	Insecure    bool   `json:"insecure"`
	HasPassword bool   `json:"hasPassword"`
	VerifiedAt  int64  `json:"verifiedAt"`
	LastError   string `json:"lastError"`
}

func (l Link) IsConfigured() bool {
	return l.URL != "" && l.Username != "" && l.HasPassword
}

type LinkPatch struct {
	URL      *string `json:"url"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	Insecure *bool   `json:"insecure"`
}

type linkSecret struct {
	URL        string `json:"url"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Insecure   bool   `json:"insecure"`
	VerifiedAt int64  `json:"verifiedAt"`
	LastError  string `json:"lastError"`
}

func (s *Service) LinkGet(ctx context.Context) (Link, error) {
	value, found, err := s.store.SettingGet(ctx, settingLink)
	if err != nil {
		return Link{}, err
	}
	link := Link{}
	if !found || strings.TrimSpace(value) == "" {
		return link, nil
	}
	plaintext, err := s.revealSettingSecret(ctx, value)
	if err != nil {
		return Link{}, err
	}
	var secret linkSecret
	if err := json.Unmarshal([]byte(plaintext), &secret); err != nil {
		return Link{}, ipc.WrapError(ipc.CodeInternal, "同步链接设置损坏", err)
	}
	link.URL = strings.TrimSpace(secret.URL)
	link.Username = strings.TrimSpace(secret.Username)
	link.Insecure = secret.Insecure
	link.HasPassword = secret.Password != ""
	link.VerifiedAt = secret.VerifiedAt
	link.LastError = secret.LastError
	return link, nil
}

func (s *Service) LinkSet(ctx context.Context, patch LinkPatch) (LinkResult, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if initialized, _ := s.vaultStatus(); !initialized {
		return LinkResult{}, ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化，无法安全保存同步链接设置；请先完成凭据保护初始化")
	}
	secret := linkSecret{}
	// 仅信封值参与逐字段合并; 历史明文整单覆写的前提是调用方每次提交完整链接。
	if value, found, err := s.store.SettingGet(ctx, settingLink); err != nil {
		return LinkResult{}, err
	} else if found && strings.HasPrefix(strings.TrimSpace(value), store.SecretEnvelopePrefix) {
		plaintext, err := s.revealSettingSecret(ctx, value)
		if err != nil {
			return LinkResult{}, err
		}
		if err := json.Unmarshal([]byte(plaintext), &secret); err != nil {
			return LinkResult{}, ipc.WrapError(ipc.CodeInternal, "同步链接设置损坏", err)
		}
	}
	if patch.URL != nil {
		secret.URL = strings.TrimSpace(*patch.URL)
	}
	if patch.Username != nil {
		secret.Username = strings.TrimSpace(*patch.Username)
	}
	if patch.Password != nil {
		secret.Password = *patch.Password
	}
	if patch.Insecure != nil {
		secret.Insecure = *patch.Insecure
	}
	encoded, err := json.Marshal(secret)
	if err != nil {
		return LinkResult{}, ipc.WrapError(ipc.CodeInternal, "无法编码同步链接设置", err)
	}
	protected, err := s.protectSettingSecret(ctx, string(encoded))
	if err != nil {
		return LinkResult{}, err
	}
	if err := s.store.SettingSet(ctx, settingLink, protected); err != nil {
		return LinkResult{}, err
	}
	s.wake()
	result := LinkResult{Link: s.linkView(secret)}
	if !result.Link.IsConfigured() {
		return result, nil
	}
	// 保存不因探测失败而回滚(与既有"先存后验"语义一致): 失败只记 LastError,
	// 成功分态 connected/mfa_required/reset_required, connected 时刷新 VerifiedAt。
	connect, err := s.engine.Connect(ctx, remoteConfigFromSecret(secret))
	if err != nil {
		s.recordProbe(ctx, err)
		return s.refreshLinkResult(ctx, result), nil
	}
	result.applyConnect(connect)
	if connect.State == LinkConnectConnected {
		s.recordProbe(ctx, nil)
		return s.refreshLinkResult(ctx, result), nil
	}
	return result, nil
}

func (s *Service) refreshLinkResult(ctx context.Context, result LinkResult) LinkResult {
	link, err := s.LinkGet(ctx)
	if err != nil {
		return result
	}
	result.Link = link
	return result
}

// LinkResult 是链接写操作与登录入职的返回: Link 视图之外附带连接三态,
// MFA/强制改密分支携带完成入职所需的票据或一次性恢复密钥。
type LinkResult struct {
	Link
	Connect      string `json:"connect"`
	MFATicket    string `json:"mfaTicket,omitempty"`
	MFAExpiresAt int64  `json:"mfaExpiresAt,omitempty"`
	RecoveryKey  string `json:"recoveryKey,omitempty"`
}

func (r *LinkResult) applyConnect(connect LinkConnectResult) {
	r.Connect = connect.State
	r.MFATicket = connect.MFATicket
	r.MFAExpiresAt = connect.MFAExpiresAt
	r.RecoveryKey = connect.RecoveryKey
}

func remoteConfigFromSecret(secret linkSecret) RemoteConfig {
	return RemoteConfig{URL: secret.URL, Username: secret.Username, Password: secret.Password, Insecure: secret.Insecure}
}

// LinkMFA 提交 TOTP 码完成两步登录入职; 票据由上一次 LinkSet 的 mfa_required 返回下发。
func (s *Service) LinkMFA(ctx context.Context, code string) (LinkResult, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	secret, err := s.linkSecret(ctx)
	if err != nil {
		return LinkResult{}, err
	}
	if strings.TrimSpace(code) == "" {
		return LinkResult{}, ipc.NewError(ipc.CodeBadParam, "验证码不能为空")
	}
	connect, err := s.engine.CompleteMFA(ctx, remoteConfigFromSecret(secret), strings.TrimSpace(code))
	if err != nil {
		return LinkResult{}, err
	}
	s.recordProbe(ctx, nil)
	s.wake()
	result := LinkResult{Link: s.linkView(secret)}
	result.applyConnect(connect)
	return result, nil
}

// LinkResetPassword 提交新密码完成强制改密入职; 成功后保存的链接口令同步更新为新口令。
func (s *Service) LinkResetPassword(ctx context.Context, newPassword string) (LinkResult, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	secret, err := s.linkSecret(ctx)
	if err != nil {
		return LinkResult{}, err
	}
	if newPassword == "" {
		return LinkResult{}, ipc.NewError(ipc.CodeBadParam, "新密码不能为空")
	}
	connect, err := s.engine.CompleteResetPassword(ctx, remoteConfigFromSecret(secret), newPassword)
	if err != nil {
		return LinkResult{}, err
	}
	secret.Password = newPassword
	if err := s.saveLinkSecret(ctx, secret); err != nil {
		return LinkResult{}, err
	}
	s.recordProbe(ctx, nil)
	s.wake()
	result := LinkResult{Link: s.linkView(secret)}
	result.applyConnect(connect)
	return result, nil
}

func (s *Service) wake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *Service) linkView(secret linkSecret) Link {
	return Link{
		URL: strings.TrimSpace(secret.URL), Username: strings.TrimSpace(secret.Username),
		Insecure: secret.Insecure, HasPassword: secret.Password != "",
		VerifiedAt: secret.VerifiedAt, LastError: secret.LastError,
	}
}

func (s *Service) saveLinkSecret(ctx context.Context, secret linkSecret) error {
	encoded, err := json.Marshal(secret)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码同步链接设置", err)
	}
	protected, err := s.protectSettingSecret(ctx, string(encoded))
	if err != nil {
		return err
	}
	return s.store.SettingSet(ctx, settingLink, protected)
}

func (s *Service) recordProbe(ctx context.Context, probeErr error) {
	secret := linkSecret{}
	value, found, err := s.store.SettingGet(ctx, settingLink)
	if err != nil || !found || strings.TrimSpace(value) == "" {
		return
	}
	plaintext, err := s.revealSettingSecret(ctx, value)
	if err != nil {
		return
	}
	if err := json.Unmarshal([]byte(plaintext), &secret); err != nil {
		return
	}
	if probeErr != nil {
		secret.LastError = probeErr.Error()
	} else {
		secret.VerifiedAt = ids.NowMS()
		secret.LastError = ""
	}
	if err := s.saveLinkSecret(ctx, secret); err != nil {
		s.engine.logger.Warn("sync probe result not recorded", "error", err)
	}
}

func (s *Service) linkSecret(ctx context.Context) (linkSecret, error) {
	secret := linkSecret{}
	value, found, err := s.store.SettingGet(ctx, settingLink)
	if err != nil {
		return secret, err
	}
	if !found || strings.TrimSpace(value) == "" {
		return secret, ipc.NewError(ipc.CodeBadParam, "同步链接未配置")
	}
	plaintext, err := s.revealSettingSecret(ctx, value)
	if err != nil {
		return secret, err
	}
	if err := json.Unmarshal([]byte(plaintext), &secret); err != nil {
		return secret, ipc.WrapError(ipc.CodeInternal, "同步链接设置损坏", err)
	}
	return secret, nil
}

func syncReportChanged(report SyncReport) bool {
	return report.Applied > 0 || report.Pushed > 0
}

// Sync 用已保存的链接执行一轮完整同步(对账 → 拉取合并 → 推送)。
func (s *Service) Sync(ctx context.Context) (report SyncReport, returnErr error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	s.roundMu.Lock()
	s.roundStatus.LastAttemptAt = time.Now()
	s.roundMu.Unlock()
	defer func() {
		s.roundMu.Lock()
		defer s.roundMu.Unlock()
		if returnErr != nil {
			s.roundStatus.LastError = returnErr.Error()
			return
		}
		s.roundStatus.LastSuccessAt = time.Now()
		s.roundStatus.LastError = ""
	}()
	secret, err := s.linkSecret(ctx)
	if err != nil {
		return SyncReport{}, err
	}
	if secret.URL == "" || secret.Username == "" || secret.Password == "" {
		return SyncReport{}, ipc.NewError(ipc.CodeBadParam, "同步链接需要同时配置 URL、用户名和口令")
	}
	report, syncErr := s.engine.Sync(ctx, RemoteConfig{
		URL: secret.URL, Username: secret.Username, Password: secret.Password, Insecure: secret.Insecure,
	})
	if syncReportChanged(report) {
		if s.profilesReload != nil {
			if err := s.profilesReload(ctx); err != nil {
				s.engine.logger.Warn("reload synced AI profiles failed", "error", err)
			}
		}
		if s.permissionsReload != nil {
			if err := s.permissionsReload(ctx); err != nil {
				s.engine.logger.Warn("reload synced AI permission failed", "error", err)
			}
		}
		_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, struct{}{})
	}
	s.recordProbe(ctx, syncErr)
	return report, syncErr
}

type Status struct {
	Configured    bool      `json:"configured"`
	LoggedIn      bool      `json:"loggedIn"`
	Username      string    `json:"username,omitempty"`
	UserID        string    `json:"userId,omitempty"`
	Head          string    `json:"head,omitempty"`
	Seq           int64     `json:"seq"`
	VerifiedAt    int64     `json:"verifiedAt"`
	LastError     string    `json:"lastError"`
	LastAttemptAt time.Time `json:"lastAttemptAt"`
	LastSuccessAt time.Time `json:"lastSuccessAt"`
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	link, err := s.LinkGet(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Configured: link.IsConfigured(), Username: link.Username,
		VerifiedAt: link.VerifiedAt, LastError: link.LastError,
	}
	s.roundMu.RLock()
	status.LastAttemptAt = s.roundStatus.LastAttemptAt
	status.LastSuccessAt = s.roundStatus.LastSuccessAt
	if s.roundStatus.LastError != "" {
		status.LastError = s.roundStatus.LastError
	}
	s.roundMu.RUnlock()
	s.engine.sessionMu.Lock()
	session := s.engine.session
	s.engine.sessionMu.Unlock()
	if session != nil {
		status.LoggedIn = true
		status.UserID = session.userID
		cursor, err := s.engine.loadCursor(ctx, session.userID)
		if err == nil {
			status.Head = cursor.Head
			status.Seq = cursor.Seq
		}
	}
	return status, nil
}
