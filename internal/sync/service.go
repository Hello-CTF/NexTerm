package sync

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	stdsync "sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

const settingLink = "sync.link"

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

type Service struct {
	store          *store.Store
	vault          *vault.Vault
	engine         *Engine
	appVersion     string
	desktop        bool
	gatewayAuthKey string

	settingsMu stdsync.Mutex
}

func (s *Service) GatewayAuthKey() string { return s.gatewayAuthKey }

func New(db *store.Store, credentialVault *vault.Vault, options ...Option) *Service {
	s := &Service{store: db, vault: credentialVault}
	for _, option := range options {
		option(s)
	}
	s.engine = NewEngine(db, credentialVault, slog.Default())
	return s
}

func (s *Service) Start(ctx context.Context) error { return nil }

func (s *Service) Shutdown(context.Context) error {
	s.engine.sessionMu.Lock()
	defer s.engine.sessionMu.Unlock()
	if s.engine.session != nil {
		clear(s.engine.session.dek)
		s.engine.session = nil
	}
	return nil
}

// ObjectHandler 暴露服务端盲存储入口, 由服务端挂载到会话中间件之后。
func (s *Service) ObjectHandler() *ObjectHandler {
	return NewObjectHandler(s.store.DB(), s.store.Backend())
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

func (s *Service) LinkSet(ctx context.Context, patch LinkPatch) (Link, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if initialized, _ := s.vaultStatus(); !initialized {
		return Link{}, ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化，无法安全保存同步链接设置；请先完成凭据保护初始化")
	}
	secret := linkSecret{}
	// 仅信封值参与逐字段合并; 历史明文整单覆写的前提是调用方每次提交完整链接。
	if value, found, err := s.store.SettingGet(ctx, settingLink); err != nil {
		return Link{}, err
	} else if found && strings.HasPrefix(strings.TrimSpace(value), store.SecretEnvelopePrefix) {
		plaintext, err := s.revealSettingSecret(ctx, value)
		if err != nil {
			return Link{}, err
		}
		if err := json.Unmarshal([]byte(plaintext), &secret); err != nil {
			return Link{}, ipc.WrapError(ipc.CodeInternal, "同步链接设置损坏", err)
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
		return Link{}, ipc.WrapError(ipc.CodeInternal, "无法编码同步链接设置", err)
	}
	protected, err := s.protectSettingSecret(ctx, string(encoded))
	if err != nil {
		return Link{}, err
	}
	if err := s.store.SettingSet(ctx, settingLink, protected); err != nil {
		return Link{}, err
	}
	return s.linkView(secret), nil
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

// Sync 用已保存的链接执行一轮完整同步(对账 → 拉取合并 → 推送)。
func (s *Service) Sync(ctx context.Context) (SyncReport, error) {
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
	s.recordProbe(ctx, syncErr)
	return report, syncErr
}

type Status struct {
	Configured bool   `json:"configured"`
	LoggedIn   bool   `json:"loggedIn"`
	Username   string `json:"username,omitempty"`
	UserID     string `json:"userId,omitempty"`
	Head       string `json:"head,omitempty"`
	Seq        int64  `json:"seq"`
	VerifiedAt int64  `json:"verifiedAt"`
	LastError  string `json:"lastError"`
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
