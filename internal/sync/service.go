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
	settingLinkSession          = "sync.link.session"
	defaultBackgroundSyncPeriod = time.Minute
	syncRoundCompleteTopic      = "sync://round-complete"
)

type Option func(*Service)

// WithMetadata retains the app version and desktop flag; the v2 protocol no longer exposes version or origin markers to the peer.
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

func WithGrantsReload(reload func(context.Context) error) Option {
	return func(s *Service) {
		s.grantsReload = reload
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
	grantsReload      func(context.Context) error
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

// syncRoundStatus is the in-memory state of the most recent Service.Sync round, shaped after store.RetentionStatus.
type syncRoundStatus struct {
	LastAttemptAt time.Time `json:"lastAttemptAt"`
	LastSuccessAt time.Time `json:"lastSuccessAt"`
	LastError     string    `json:"lastError,omitempty"`
	Warnings      []string  `json:"warnings,omitempty"`
	Paused        bool      `json:"paused"`
	VaultLocked   bool      `json:"vaultLocked"`
}

func (s *Service) GatewayAuthKey() string { return s.gatewayAuthKey }

func (s *Service) SetPermissionsReload(reload func(context.Context) error) {
	s.permissionsReload = reload
}

func (s *Service) SetGrantsReload(reload func(context.Context) error) {
	s.grantsReload = reload
}

func New(db *store.Store, credentialVault *vault.Vault, options ...Option) *Service {
	s := &Service{store: db, vault: credentialVault, syncPeriod: defaultBackgroundSyncPeriod, wakeCh: make(chan struct{}, 1)}
	for _, option := range options {
		option(s)
	}
	s.engine = NewEngine(db, credentialVault, s.logger)
	s.engine.aiShared = s.desktop
	if credentialVault != nil {
		// Wake a background sync round immediately after the vault unlocks: a vault locked at startup no longer waits for the next period.
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
			s.roundMu.RLock()
			wasBlocked := s.blockedNoticed
			hadAttempt := !s.roundStatus.LastAttemptAt.IsZero()
			s.roundMu.RUnlock()
			s.noteSyncBlocked(appErr)
			// Do not emit a startup event for a vault that has never been readable. Once an active sync pauses,
			// notify listeners once so they can refresh the paused state without treating it as a failed round.
			if hadAttempt && !wasBlocked {
				_ = ipc.Emit(ctx, s.events, syncRoundCompleteTopic, struct{}{})
			}
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
	_, syncErr := s.Sync(ctx)
	if syncErr != nil && ctx.Err() == nil {
		if paused, _ := vaultPauseState(syncErr); !paused {
			s.engine.logger.Warn("background sync failed", "error", syncErr)
		}
	}
	_ = ipc.Emit(ctx, s.events, syncRoundCompleteTopic, struct{}{})
	// Broadcast status when a background round establishes a new session (unlock wake/failure recovery), so an open settings page auto-refreshes to logged-in.
	if !wasLoggedIn && s.engine.hasSession() {
		_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, ipc.SyncStatusEvent{OwnerID: ipc.OwnerID(ctx)})
	}
}

func vaultPauseState(err error) (paused, locked bool) {
	var appErr *ipc.Error
	if !errors.As(err, &appErr) {
		return false, false
	}
	switch appErr.Code {
	case ipc.CodeVaultLocked:
		return true, true
	case ipc.CodeVaultNotInit:
		return true, false
	default:
		return false, false
	}
}

// noteSyncBlocked records a background sync pause caused by a locked/uninitialized vault: it writes the round status so Status is visible
// (previously a silent skip with no way for the user to know sync stalled), and logs only one Warn per contiguous pause period; after unlock recovery,
// roundStatus.LastError is cleared by the next successful round's defer.
func (s *Service) noteSyncBlocked(err error) {
	_, locked := vaultPauseState(err)
	s.roundMu.Lock()
	s.roundStatus.LastAttemptAt = time.Now()
	s.roundStatus.LastError = err.Error()
	s.roundStatus.Warnings = nil
	s.roundStatus.Paused = true
	s.roundStatus.VaultLocked = locked
	noticed := s.blockedNoticed
	s.blockedNoticed = true
	s.roundMu.Unlock()
	if !noticed {
		s.engine.logger.Warn("background sync paused: credential vault locked or not initialized", "error", err)
	}
}

// clearSyncBlocked resets the pause latch once the link is readable (vault available): entering the paused state again logs anew.
func (s *Service) clearSyncBlocked() {
	s.roundMu.Lock()
	s.blockedNoticed = false
	if s.roundStatus.Paused {
		s.roundStatus.LastError = ""
	}
	s.roundStatus.Paused = false
	s.roundStatus.VaultLocked = false
	s.roundMu.Unlock()
}

// probeSavedLink runs one connection probe against the saved link at startup: a valid persisted session (saved by the last process) restores
// without login (no re-login, no DEK envelope unwrap, no MFA challenge for MFA accounts); a missing/invalid persisted session falls back to the
// same password probe path as LinkSet: connected establishes the session and refreshes VerifiedAt; mfa_required/reset_required cannot complete
// automatically and are recorded in LastError to guide an interactive re-login; probe failures only record LastError and do not block startup.
// When unconfigured or the vault is unavailable (locked/uninitialized), it silently skips and leaves over to the background loop and unlock wake.
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
	if s.probePersistedSession(ctx, secret) {
		s.recordProbe(ctx, nil)
		_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, ipc.SyncStatusEvent{OwnerID: ipc.OwnerID(ctx)})
		return
	}
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
	_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, ipc.SyncStatusEvent{OwnerID: ipc.OwnerID(ctx)})
}

// probePersistedSession prefers restoring the account session from the persisted session: a passing me() check restores without login.
// When the envelope does not match the current link identity (link reconfigured) or the server has revoked the session (401), it clears the envelope and falls back
// to password re-login; other failures such as network errors keep the envelope and also fall back to password re-login.
func (s *Service) probePersistedSession(ctx context.Context, secret linkSecret) bool {
	persisted, found := s.engine.loadPersistedSession(ctx)
	if !found {
		return false
	}
	if persisted.URL != secret.URL || persisted.Username != secret.Username {
		s.engine.clearPersistedSession(ctx)
		return false
	}
	if _, err := s.engine.restoreSession(ctx, remoteConfigFromSecret(secret), persisted); err != nil {
		if errors.Is(err, errSessionExpired) {
			s.engine.clearPersistedSession(ctx)
		}
		return false
	}
	return true
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

// ObjectHandler exposes the server-side blind-store entry, mounted by the server after the session middleware.
// It carries the service emitter so a successful push wakes the pusher's other sessions without a poll round.
func (s *Service) ObjectHandler() *ObjectHandler {
	handler := NewObjectHandler(s.store.GORM(), s.engine.logger)
	handler.events = s.events
	return handler
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
	notifyStatus := false
	defer func() {
		s.settingsMu.Unlock()
		if notifyStatus {
			_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, ipc.SyncStatusEvent{OwnerID: ipc.OwnerID(ctx)})
		}
	}()
	if initialized, _ := s.vaultStatus(); !initialized {
		return LinkResult{}, ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化，无法安全保存同步链接设置；请先完成凭据保护初始化")
	}
	secret := linkSecret{}
	// Only envelope values participate in field-by-field merging; wholesale overwrite of legacy plaintext relies on the caller submitting the complete link every time.
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
	previous := secret
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
	changed := secret.URL != previous.URL || secret.Username != previous.Username ||
		secret.Password != previous.Password || secret.Insecure != previous.Insecure
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
	if changed {
		// The new link is already saved. A failed login must not leave the old
		// account or old password's cookie available to desktop commands.
		s.engine.resetSession()
		s.engine.clearPersistedSession(ctx)
	}
	notifyStatus = true
	s.wake()
	result := LinkResult{Link: s.linkView(secret)}
	if !result.Link.IsConfigured() {
		return result, nil
	}
	// Saving is not rolled back on probe failure (same "save first, verify second" semantics as before): failure only records LastError,
	// success splits into connected/mfa_required/reset_required, with VerifiedAt refreshed on connected.
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

// LinkResult is the return of link write operations and login onboarding: the Link view plus the connection tri-state,
// with the MFA/forced-password-change branches carrying the ticket or one-time recovery key needed to finish onboarding.
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

// LinkMFA submits a TOTP code to complete two-step login onboarding; the ticket is issued by the previous LinkSet's mfa_required return.
func (s *Service) LinkMFA(ctx context.Context, code string) (LinkResult, error) {
	s.settingsMu.Lock()
	notifyStatus := false
	defer func() {
		s.settingsMu.Unlock()
		if notifyStatus {
			_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, ipc.SyncStatusEvent{OwnerID: ipc.OwnerID(ctx)})
		}
	}()
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
	notifyStatus = true
	result := LinkResult{Link: s.linkView(secret)}
	result.applyConnect(connect)
	return result, nil
}

// LinkResetPassword submits a new password to complete forced-password-change onboarding; on success the saved link password is updated to the new password.
func (s *Service) LinkResetPassword(ctx context.Context, newPassword string) (LinkResult, error) {
	s.settingsMu.Lock()
	notifyStatus := false
	defer func() {
		s.settingsMu.Unlock()
		if notifyStatus {
			_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, ipc.SyncStatusEvent{OwnerID: ipc.OwnerID(ctx)})
		}
	}()
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
	notifyStatus = true
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

// Sync runs one full sync round (reconcile -> pull and merge -> push) with the saved link.
func (s *Service) Sync(ctx context.Context) (report SyncReport, returnErr error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	s.roundMu.Lock()
	s.roundStatus.LastAttemptAt = time.Now()
	s.roundStatus.Warnings = nil
	s.roundStatus.Paused = false
	s.roundStatus.VaultLocked = false
	s.roundMu.Unlock()
	defer func() {
		s.roundMu.Lock()
		defer s.roundMu.Unlock()
		s.roundStatus.Warnings = append([]string(nil), report.Warnings...)
		if returnErr != nil {
			s.roundStatus.LastError = returnErr.Error()
			s.roundStatus.Paused, s.roundStatus.VaultLocked = vaultPauseState(returnErr)
			return
		}
		s.roundStatus.LastSuccessAt = time.Now()
		s.roundStatus.LastError = ""
		s.roundStatus.Paused = false
		s.roundStatus.VaultLocked = false
		s.blockedNoticed = false
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
		if s.grantsReload != nil {
			if err := s.grantsReload(ctx); err != nil {
				s.engine.logger.Warn("reload synced AI grants failed", "error", err)
			}
		}
		_ = ipc.Emit(ctx, s.events, ipc.TopicSyncStatus, ipc.SyncStatusEvent{OwnerID: ipc.OwnerID(ctx)})
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
	Warnings      []string  `json:"warnings,omitempty"`
	Paused        bool      `json:"paused"`
	VaultLocked   bool      `json:"vaultLocked"`
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	link, linkErr := s.LinkGet(ctx)
	status := Status{}
	if linkErr != nil {
		paused, locked := vaultPauseState(linkErr)
		if !paused {
			return Status{}, linkErr
		}
		status.Paused = true
		status.VaultLocked = locked
		// The encrypted link cannot be decoded while locked, but its presence is still readable.
		// This lets the status bar distinguish a configured paused sync from local-only mode.
		if raw, found, err := s.store.SettingGet(ctx, settingLink); err == nil {
			status.Configured = found && strings.TrimSpace(raw) != ""
		}
	} else {
		status.Configured = link.IsConfigured()
		status.Username = link.Username
		status.VerifiedAt = link.VerifiedAt
		status.LastError = link.LastError
	}
	s.roundMu.RLock()
	status.LastAttemptAt = s.roundStatus.LastAttemptAt
	status.LastSuccessAt = s.roundStatus.LastSuccessAt
	status.Warnings = append([]string(nil), s.roundStatus.Warnings...)
	if s.roundStatus.LastError != "" {
		status.LastError = s.roundStatus.LastError
	}
	if s.roundStatus.Paused {
		status.Paused = true
		status.VaultLocked = s.roundStatus.VaultLocked
	}
	s.roundMu.RUnlock()
	if status.Paused {
		return status, nil
	}
	s.engine.sessionMu.Lock()
	session := s.engine.session
	s.engine.sessionMu.Unlock()
	if session != nil && link.IsConfigured() && session.key == link.URL+"\x00"+link.Username {
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
