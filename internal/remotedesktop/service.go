package remotedesktop

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	dialTimeout    = 15 * time.Second
	pendingCertTTL = 10 * time.Minute
)

type liveConnection struct {
	info         RemoteDesktopConnectionInfo
	owner        string
	loginSession string
	cancel       context.CancelFunc
	checkCtx     context.Context
}

type pendingCert struct {
	status   RemoteDesktopCertificateStatus
	expires  time.Time
	hostPort string
}

type Option func(*Service)

func WithDialerFactory(factory DialerFactory) Option {
	return func(s *Service) { s.newDialer = factory }
}
func WithCertificateStore(store CertificateStore) Option {
	return func(s *Service) { s.certs = store }
}
func WithAuditor(auditor Auditor) Option    { return func(s *Service) { s.auditor = auditor } }
func WithRDPEngine(engine RDPEngine) Option { return func(s *Service) { s.rdp = engine } }
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}
func WithRecheckInterval(d time.Duration) Option  { return func(s *Service) { s.recheck = d } }
func WithHandshakeTimeout(d time.Duration) Option { return func(s *Service) { s.handshakeTimeout = d } }
func WithTicketTTL(ttl time.Duration) Option      { return func(s *Service) { s.ttl = ttl } }
func WithLogger(logger *slog.Logger) Option       { return func(s *Service) { s.logger = logger } }

// Service owns tickets, certificate trust and live relay connections.
type Service struct {
	resolver  AssetResolver
	newDialer DialerFactory
	certs     CertificateStore
	auditor   Auditor
	rdp       RDPEngine
	logger    *slog.Logger
	now       func() time.Time
	ttl       time.Duration

	tickets *ticketStore

	// recheck is how often live connections re-validate their login session; handshakeTimeout bounds the first
	// frame and the upstream security handshake.
	recheck          time.Duration
	handshakeTimeout time.Duration

	mu              sync.Mutex
	conns           map[string]*liveConnection
	pending         map[string]*pendingCert
	revoked         map[string]time.Time
	userRevocations ipc.UserRevocations
	closed          bool
}

func NewService(resolver AssetResolver, options ...Option) *Service {
	s := &Service{
		resolver: resolver, rdp: rdCleanPathEngine{}, logger: slog.Default(), now: time.Now,
		conns: map[string]*liveConnection{}, pending: map[string]*pendingCert{}, revoked: map[string]time.Time{}, recheck: 30 * time.Second, handshakeTimeout: 30 * time.Second,
	}
	for _, option := range options {
		option(s)
	}
	s.tickets = newTicketStore(s.now, s.ttl)
	return s
}

func (s *Service) Start(context.Context) error { return nil }
func (s *Service) Shutdown(context.Context) error {
	s.Close()
	return nil
}

// Close ends every live connection.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	conns := make([]*liveConnection, 0, len(s.conns))
	for _, c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.cancel()
	}
}

// CloseAccountSession ends the connections of a revoked login session and invalidates its tickets.
func (s *Service) CloseAccountSession(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	s.tickets.dropLoginSession(sessionID)
	s.mu.Lock()
	s.pruneRevokedLocked()
	s.revoked[sessionID] = s.now()
	var victims []*liveConnection
	for _, c := range s.conns {
		if c.loginSession == sessionID {
			victims = append(victims, c)
		}
	}
	s.mu.Unlock()
	for _, c := range victims {
		c.cancel()
	}
	return nil
}

// revokedTTL is far longer than a ticket lives: a revoked session cannot obtain a new ticket or connection after
// its tickets expired, so older entries carry no information.
const revokedTTL = time.Hour

func (s *Service) pruneRevokedLocked() {
	cutoff := s.now().Add(-revokedTTL)
	for id, at := range s.revoked {
		if at.Before(cutoff) {
			delete(s.revoked, id)
		}
	}
}

// CloseAccountUser ends the connections and tickets of a user's login sessions other than exceptSession, for
// revocations that do not enumerate session ids (password change, recovery reset, disabling the account).
func (s *Service) CloseAccountUser(userID, exceptSession string) {
	if userID == "" {
		return
	}
	s.tickets.dropUser(userID, exceptSession)
	s.mu.Lock()
	s.userRevocations.Revoke(userID, exceptSession)
	var victims []*liveConnection
	for _, c := range s.conns {
		if c.owner == userID && ipc.MatchesRevoked(c.loginSession, exceptSession) {
			victims = append(victims, c)
		}
	}
	s.mu.Unlock()
	for _, c := range victims {
		c.cancel()
	}
}

func pendingKey(owner, assetID string) string { return owner + "\x00" + assetID }

func (s *Service) loadAsset(ctx context.Context, assetID string) (Asset, Options, error) {
	if s.resolver == nil {
		return Asset{}, Options{}, ipc.NewError(ipc.CodeUnsupported, "远程桌面服务未配置")
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return Asset{}, Options{}, ipc.NewError(ipc.CodeBadParam, "资产 ID 不能为空")
	}
	asset, err := s.resolver.ResolveAsset(ctx, assetID)
	if err != nil {
		return Asset{}, Options{}, err
	}
	if owner := ipc.OwnerID(ctx); owner != "" && asset.OwnerID != owner {
		return Asset{}, Options{}, ipc.NewError(ipc.CodeForbidden, "无权访问该资产")
	}
	if asset.Kind != KindRDP && asset.Kind != KindVNC {
		return Asset{}, Options{}, ipc.NewError(ipc.CodeBadParam, "该资产不是远程桌面（RDP/VNC）资产")
	}
	if strings.TrimSpace(asset.Host) == "" {
		return Asset{}, Options{}, ipc.NewError(ipc.CodeBadParam, "资产没有填写主机地址，请先补全")
	}
	if asset.Port == 0 {
		asset.Port = kindPort(asset.Kind)
	}
	if asset.Port < 1 || asset.Port > 65535 {
		return Asset{}, Options{}, ipc.NewError(ipc.CodeBadParam, "资产端口无效")
	}
	options, err := ParseOptions(asset.OptionsJSON)
	if err != nil {
		return Asset{}, Options{}, err
	}
	return asset, options, nil
}

// Prepare checks the account session and asset ownership and issues a single-use ticket. The destination is never
// taken from the caller.
func (s *Service) Prepare(ctx context.Context, assetID string) (RemoteDesktopPrepareResult, error) {
	if err := ipc.CheckAccountSession(ctx); err != nil {
		return RemoteDesktopPrepareResult{}, err
	}
	loginSession := ipc.AccountSessionID(ctx)
	s.mu.Lock()
	s.pruneRevokedLocked()
	_, revoked := s.revoked[loginSession]
	closed := s.closed
	s.mu.Unlock()
	if closed || (loginSession != "" && revoked) {
		return RemoteDesktopPrepareResult{}, ipc.NewError(ipc.CodeForbidden, "登录会话已失效")
	}
	asset, options, err := s.loadAsset(ctx, assetID)
	if err != nil {
		return RemoteDesktopPrepareResult{}, err
	}
	t, err := s.tickets.issue(context.WithoutCancel(ctx), ipc.OwnerID(ctx), loginSession, asset.ID, asset.Kind)
	if err != nil {
		return RemoteDesktopPrepareResult{}, err
	}
	return RemoteDesktopPrepareResult{
		Ticket: t.token, Protocol: asset.Kind, AssetID: asset.ID, Name: asset.Name, Host: asset.Host, Port: asset.Port,
		Username: asset.Username, Domain: options.Domain, PromptPassword: options.PromptPassword,
		HasPassword: asset.HasPassword && !options.PromptPassword,
		NoPassword:  asset.AuthNone && !asset.HasPassword && !options.PromptPassword, Width: options.Width, Height: options.Height,
		ExpiresAtMs: t.expires.UnixMilli(),
	}, nil
}

// SetPassword binds the password a user typed for a promptPassword VNC asset to its ticket. It stays in Go memory,
// is consumed by the connection that uses the ticket, and is never logged or audited.
func (s *Service) SetPassword(ctx context.Context, token, password string) error {
	if err := ipc.CheckAccountSession(ctx); err != nil {
		return err
	}
	if password == "" || len(password) > 1024 {
		return ipc.NewError(ipc.CodeBadParam, "密码不能为空且长度不能超过 1024")
	}
	t, err := s.tickets.setPassword(token, ipc.OwnerID(ctx), ipc.AccountSessionID(ctx), KindVNC, password)
	if err != nil {
		return err
	}
	_, options, err := s.loadAsset(ctx, t.assetID)
	if err != nil {
		return err
	}
	if !options.PromptPassword {
		s.tickets.clearPassword(token)
		return ipc.NewError(ipc.CodeBadParam, "该资产没有设置连接时输入密码")
	}
	return nil
}

// ReleaseSecret hands the RDP password to the WebView (IronRDP does CredSSP in WASM). It is ticket bound, works once
// per ticket, is audited before the secret leaves Go, and is never logged.
func (s *Service) ReleaseSecret(ctx context.Context, token string) (RemoteDesktopSecretResult, error) {
	if err := ipc.CheckAccountSession(ctx); err != nil {
		return RemoteDesktopSecretResult{}, err
	}
	t, err := s.tickets.release(token, ipc.OwnerID(ctx), ipc.AccountSessionID(ctx), KindRDP)
	if err != nil {
		s.audit(ctx, AuditFailure, "", map[string]any{"action": "secret_release", "reason": "ticket"})
		return RemoteDesktopSecretResult{}, err
	}
	deny := func(reason string, err error) (RemoteDesktopSecretResult, error) {
		s.audit(ctx, AuditFailure, t.assetID, map[string]any{"action": "secret_release", "reason": reason})
		return RemoteDesktopSecretResult{}, err
	}
	asset, options, err := s.loadAsset(ctx, t.assetID)
	if err != nil {
		return deny("asset", err)
	}
	if asset.Kind != KindRDP {
		return deny("kind", ipc.NewError(ipc.CodeForbidden, "仅 RDP 资产可以释放凭据"))
	}
	if options.PromptPassword {
		return deny("prompt", ipc.NewError(ipc.CodeForbidden, "该资产设置为连接时输入密码，不会释放已保存的凭据"))
	}
	password := ""
	if asset.HasPassword {
		if password, err = s.resolver.ResolvePassword(ctx, asset.ID); err != nil {
			return deny("decrypt", err)
		}
	}
	if err := s.auditStrict(ctx, AuditSecretRelease, asset.ID, map[string]any{"protocol": asset.Kind, "hasPassword": password != ""}); err != nil {
		return RemoteDesktopSecretResult{}, ipc.WrapError(ipc.CodeInternal, "写入审计日志失败，已拒绝释放凭据", err)
	}
	return RemoteDesktopSecretResult{Username: asset.Username, Domain: options.Domain, Password: password}, nil
}

func (s *Service) audit(ctx context.Context, kind, assetID string, payload map[string]any) {
	if err := s.auditStrict(ctx, kind, assetID, payload); err != nil {
		s.logger.Warn("remote desktop audit failed", "kind", kind, "error", err)
	}
}

func (s *Service) auditStrict(ctx context.Context, kind, assetID string, payload map[string]any) error {
	if s.auditor == nil {
		return nil
	}
	return s.auditor.Audit(ctx, AuditEvent{Kind: kind, AssetID: assetID, Payload: payload})
}

// List returns the caller's live connections.
func (s *Service) List(ctx context.Context) []RemoteDesktopConnectionInfo {
	owner := ipc.OwnerID(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []RemoteDesktopConnectionInfo{}
	for _, c := range s.conns {
		if owner == "" || c.owner == owner {
			out = append(out, c.info)
		}
	}
	return out
}

// Close ends one of the caller's connections.
func (s *Service) CloseConnection(ctx context.Context, id string) error {
	owner := ipc.OwnerID(ctx)
	s.mu.Lock()
	c := s.conns[id]
	s.mu.Unlock()
	if c == nil {
		return nil
	}
	if owner != "" && c.owner != owner {
		return ipc.NewError(ipc.CodeForbidden, "无权关闭该远程桌面连接")
	}
	c.cancel()
	return nil
}

func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// verifyCertificate applies trust on first use. It never accepts an unknown or changed certificate: the observation
// is parked for an explicit Trust call and the connection is refused.
func (s *Service) verifyCertificate(ctx context.Context, asset Asset, chain [][]byte, leaf *x509.Certificate) error {
	if s.certs == nil {
		return ipc.NewError(ipc.CodeUnsupported, "证书信任存储未配置，无法验证服务器证书")
	}
	if len(chain) == 0 {
		return userErr("服务器没有提供证书")
	}
	fingerprint := fingerprintOf(chain[0])
	keyType := certKeyType(asset.Kind)
	pinned, found, err := s.certs.Get(ctx, asset.Host, asset.Port, keyType)
	if err != nil {
		return err
	}
	owner := ipc.OwnerID(ctx)
	key := pendingKey(owner, asset.ID)
	if found && pinned == fingerprint {
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
		return nil
	}
	status := RemoteDesktopCertificateStatus{
		AssetID: asset.ID, Protocol: asset.Kind, Host: asset.Host, Port: asset.Port, Status: CertNew,
		Fingerprint: fingerprint, ObservedAtMs: s.now().UnixMilli(),
	}
	if leaf != nil {
		status.Subject = leaf.Subject.String()
		status.NotAfterMs = leaf.NotAfter.UnixMilli()
	}
	message := fmt.Sprintf("首次连接 %s:%d，请核对并确认服务器证书指纹 %s", asset.Host, asset.Port, fingerprint)
	if found {
		status.Status = CertChanged
		status.PreviousFingerprint = pinned
		message = fmt.Sprintf("服务器 %s:%d 的证书已变更（原指纹 %s，现指纹 %s），连接已阻止；确认可信后需手动重新确认", asset.Host, asset.Port, pinned, fingerprint)
	}
	s.mu.Lock()
	s.pending[key] = &pendingCert{status: status, expires: s.now().Add(pendingCertTTL)}
	s.mu.Unlock()
	return ipc.NewError(ipc.CodeNeedsConfirm, message).WithDetail(status)
}

// RemoteDesktopCertificateStatus reports the last certificate observed for the asset (new/changed), or the pinned one.
func (s *Service) CertificateStatus(ctx context.Context, assetID string) (RemoteDesktopCertificateStatus, error) {
	asset, _, err := s.loadAsset(ctx, assetID)
	if err != nil {
		return RemoteDesktopCertificateStatus{}, err
	}
	s.mu.Lock()
	pending := s.pending[pendingKey(ipc.OwnerID(ctx), asset.ID)]
	if pending != nil && !s.now().Before(pending.expires) {
		delete(s.pending, pendingKey(ipc.OwnerID(ctx), asset.ID))
		pending = nil
	}
	s.mu.Unlock()
	if pending != nil && pending.status.Host == asset.Host && pending.status.Port == asset.Port {
		return pending.status, nil
	}
	status := RemoteDesktopCertificateStatus{AssetID: asset.ID, Protocol: asset.Kind, Host: asset.Host, Port: asset.Port, Status: CertNone}
	if s.certs != nil {
		fingerprint, found, err := s.certs.Get(ctx, asset.Host, asset.Port, certKeyType(asset.Kind))
		if err != nil {
			return RemoteDesktopCertificateStatus{}, err
		}
		if found {
			status.Status, status.Fingerprint = CertPinned, fingerprint
		}
	}
	return status, nil
}

// TrustCertificate pins the fingerprint last observed for the asset. The caller must echo that exact fingerprint,
// so a certificate the user never saw cannot be pinned.
func (s *Service) TrustCertificate(ctx context.Context, assetID, fingerprint string) (RemoteDesktopCertificateStatus, error) {
	asset, _, err := s.loadAsset(ctx, assetID)
	if err != nil {
		return RemoteDesktopCertificateStatus{}, err
	}
	if s.certs == nil {
		return RemoteDesktopCertificateStatus{}, ipc.NewError(ipc.CodeUnsupported, "证书信任存储未配置")
	}
	key := pendingKey(ipc.OwnerID(ctx), asset.ID)
	s.mu.Lock()
	pending := s.pending[key]
	s.mu.Unlock()
	if pending == nil || !s.now().Before(pending.expires) {
		return RemoteDesktopCertificateStatus{}, ipc.NewError(ipc.CodeBadParam, "没有待确认的证书，请先发起连接以获取服务器证书指纹")
	}
	if pending.status.Host != asset.Host || pending.status.Port != asset.Port {
		return RemoteDesktopCertificateStatus{}, ipc.NewError(ipc.CodeBadParam, "资产地址已修改，请重新连接以获取新的证书指纹")
	}
	if fingerprint != pending.status.Fingerprint {
		return RemoteDesktopCertificateStatus{}, ipc.NewError(ipc.CodeBadParam, "证书指纹与最近一次连接观察到的不一致，请重新连接")
	}
	if err := s.certs.Accept(ctx, asset.Host, asset.Port, certKeyType(asset.Kind), fingerprint); err != nil {
		return RemoteDesktopCertificateStatus{}, err
	}
	s.mu.Lock()
	if s.pending[key] == pending {
		delete(s.pending, key)
	}
	s.mu.Unlock()
	s.audit(ctx, AuditCertTrust, asset.ID, map[string]any{
		"protocol": asset.Kind, "host": asset.Host, "port": asset.Port, "fingerprint": fingerprint,
		"previousFingerprint": pending.status.PreviousFingerprint, "change": pending.status.Status,
	})
	status := pending.status
	status.Status = CertPinned
	return status, nil
}

func (s *Service) dialFunc(ctx context.Context, asset Asset, options Options) (func(context.Context) (net.Conn, error), error) {
	addr := joinAddr(asset.Host, asset.Port)
	if options.ViaAssetID == "" {
		dialer := &net.Dialer{Timeout: dialTimeout}
		return func(ctx context.Context) (net.Conn, error) { return dialer.DialContext(ctx, "tcp", addr) }, nil
	}
	if s.newDialer == nil {
		return nil, ipc.NewError(ipc.CodeUnsupported, "SSH 跳板拨号未配置")
	}
	dial, err := s.newDialer(ctx, options.ViaAssetID)
	if err != nil {
		return nil, err
	}
	if dial == nil {
		return nil, ipc.NewError(ipc.CodeUnsupported, "SSH 跳板拨号器未配置")
	}
	return func(ctx context.Context) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, dialTimeout)
		defer cancel()
		return dial(ctx, "tcp", addr)
	}, nil
}

// Serve terminates one frame transport. The ticket arrives either as ticketHint (browser mode query) or in the
// first frame: either TicketPreamble+ticket, or an RDCleanPath request whose proxy_auth is the ticket.
func (s *Service) Serve(ctx context.Context, fc FrameConn, ident Identity, ticketHint string) error {
	defer fc.Close()
	token := strings.TrimSpace(ticketHint)
	var first []byte
	if token == "" {
		timer := time.AfterFunc(s.handshakeTimeout, func() { _ = fc.Close() })
		frame, err := fc.ReadFrame()
		timer.Stop()
		if err != nil {
			return err
		}
		if len(frame) > FirstFrameLimit {
			return ipc.NewError(ipc.CodeBadParam, "首帧过大")
		}
		if strings.HasPrefix(string(frame), TicketPreamble) {
			token = strings.TrimSpace(string(frame[len(TicketPreamble):]))
		} else if pdu, perr := parseRDCleanPath(frame); perr == nil && pdu.HasProxyAuth {
			token, first = pdu.ProxyAuth, frame
		}
		if token == "" {
			_ = fc.WriteFrame(newRDCleanPathHTTPError(401))
			return errTicketInvalid()
		}
	}
	userEpoch := s.userRevocations.Epoch(ident.UserID)
	t, err := s.tickets.consume(token, ident.UserID, ident.LoginSessionID)
	if err != nil {
		if first != nil {
			_ = fc.WriteFrame(newRDCleanPathHTTPError(403))
		}
		return err
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if t.userID != "" {
		connCtx = ipc.WithUserID(connCtx, t.userID)
	}
	id := ids.New()
	live := &liveConnection{
		info:  RemoteDesktopConnectionInfo{ID: id, AssetID: t.assetID, Protocol: t.protocol, StartedMs: s.now().UnixMilli()},
		owner: t.userID, loginSession: t.loginSession, cancel: cancel, checkCtx: t.checkCtx,
	}
	s.mu.Lock()
	s.pruneRevokedLocked()
	_, revoked := s.revoked[t.loginSession]
	if s.closed || (t.loginSession != "" && revoked) || s.userRevocations.Stale(t.userID, userEpoch, t.loginSession) {
		s.mu.Unlock()
		return ipc.NewError(ipc.CodeForbidden, "登录会话已失效")
	}
	s.conns[id] = live
	s.mu.Unlock()
	if s.recheck > 0 && t.checkCtx != nil {
		go s.watchLoginSession(connCtx, t.checkCtx, cancel)
	}
	defer func() {
		s.mu.Lock()
		delete(s.conns, id)
		s.mu.Unlock()
	}()
	// A closed connection (logout, close command, shutdown) must unblock pending reads and writes.
	stop := context.AfterFunc(connCtx, func() { _ = fc.Close() })
	defer stop()

	started := s.now()
	fail := func(err error) error {
		s.logger.Warn("remote desktop connection failed", "asset", t.assetID, "protocol", t.protocol, "error", err)
		s.audit(connCtx, AuditFailure, t.assetID, map[string]any{"protocol": t.protocol, "error": errorSummary(err)})
		return err
	}
	asset, options, err := s.loadAsset(connCtx, t.assetID)
	if err != nil {
		return fail(err)
	}
	if asset.Kind != t.protocol {
		return fail(ipc.NewError(ipc.CodeForbidden, "资产类型已变化，请重新连接"))
	}
	dial, err := s.dialFunc(connCtx, asset, options)
	if err != nil {
		return fail(err)
	}
	verify := func(chain [][]byte, leaf *x509.Certificate) error {
		return s.verifyCertificate(connCtx, asset, chain, leaf)
	}
	connected := false
	tlsVersion := ""
	switch asset.Kind {
	case KindRDP:
		err = s.rdp.Serve(connCtx, RDPSession{
			Client: fc, First: first, Dial: dial, Addr: joinAddr(asset.Host, asset.Port), VerifyCert: verify, LegacyTLS: options.LegacyTLS,
			Dialed: func() {
				connected = true
				s.audit(connCtx, AuditConnect, asset.ID, map[string]any{"protocol": KindRDP, "host": asset.Host, "port": asset.Port, "viaAssetId": options.ViaAssetID, "legacyTLS": options.LegacyTLS})
			},
			Secured: func(version uint16) { tlsVersion = tls.VersionName(version) },
		})
	case KindVNC:
		err = s.serveVNC(connCtx, fc, asset, options, t.password, dial, verify, func() {
			connected = true
			s.audit(connCtx, AuditConnect, asset.ID, map[string]any{"protocol": KindVNC, "host": asset.Host, "port": asset.Port, "viaAssetId": options.ViaAssetID})
		})
	}
	if connected {
		payload := map[string]any{"protocol": asset.Kind, "durationMs": s.now().Sub(started).Milliseconds(), "error": errorSummary(err)}
		if tlsVersion != "" {
			payload["tlsVersion"] = tlsVersion
		}
		s.audit(context.WithoutCancel(connCtx), AuditDisconnect, asset.ID, payload)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		if !connected {
			return fail(err)
		}
		s.audit(context.WithoutCancel(connCtx), AuditFailure, asset.ID, map[string]any{"protocol": asset.Kind, "error": errorSummary(err)})
	}
	return err
}

// watchLoginSession ends a live connection once its login session is confirmed invalid (natural expiry, disabled
// account, password change, recovery reset). Transient check failures keep the connection.
func (s *Service) watchLoginSession(connCtx, checkCtx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(s.recheck)
	defer ticker.Stop()
	for {
		select {
		case <-connCtx.Done():
			return
		case <-ticker.C:
		}
		var appErr *ipc.Error
		if err := ipc.CheckAccountSession(checkCtx); errors.As(err, &appErr) && appErr.Code == ipc.CodeForbidden {
			cancel()
			return
		}
	}
}

func errorSummary(err error) string {
	if err == nil {
		return ""
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	var ue *userError
	if errors.As(err, &ue) {
		return ue.msg
	}
	return "连接错误"
}

func (s *Service) serveVNC(ctx context.Context, fc FrameConn, asset Asset, options Options, typedPassword string, dial func(context.Context) (net.Conn, error), verify func([][]byte, *x509.Certificate) error, connected func()) error {
	client := &frameStream{fc: fc}
	password := ""
	if options.PromptPassword {
		if typedPassword == "" {
			vncRejectClient(client, "该资产设置为连接时输入密码，请先输入密码")
			return ipc.NewError(ipc.CodeBadParam, "未提供 VNC 密码")
		}
		password = typedPassword
	} else if asset.HasPassword {
		var err error
		if password, err = s.resolver.ResolvePassword(ctx, asset.ID); err != nil {
			vncRejectClient(client, userMessage(err))
			return err
		}
	}
	// A pinned certificate means this host has offered TLS before: never fall back to a plain security type.
	_, pinned, err := s.certs.Get(ctx, asset.Host, asset.Port, certKeyTypeVNC)
	if err != nil {
		vncRejectClient(client, userMessage(err))
		return err
	}
	upstream, err := vncDialUpstream(ctx, dial, vncUpstreamConfig{
		Username: asset.Username, Password: password, ServerName: asset.Host,
		VerifyCert: verify, RequireTLS: pinned, Timeout: s.handshakeTimeout,
	})
	if err != nil {
		vncRejectClient(client, userMessage(err))
		return err
	}
	connected()
	defer upstream.Close()
	if err := vncFacadeHandshake(client); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
			return nil // the viewer went away during the handshake
		}
		return err
	}
	return relayFrames(fc, upstream, client)
}

func userMessage(err error) string {
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	var ue *userError
	if errors.As(err, &ue) {
		return ue.msg
	}
	return "无法连接远程桌面"
}
