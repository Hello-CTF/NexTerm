package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type Service struct {
	provider         DialerProvider
	sessionHost      func(context.Context, string) (string, error)
	rules            RuleStore
	isProduction     func(context.Context, string) (bool, error)
	env              Environment
	reconnect        ReconnectPolicy
	dialTimeout      time.Duration
	handshakeTimeout time.Duration
	closeTimeout     time.Duration
	now              func() time.Time
	onError          func(error)
	listen           func(string, string) (net.Listener, error)

	ctx       context.Context
	cancel    context.CancelFunc
	ruleMu    sync.Mutex
	mu        sync.Mutex
	forwards  map[string]*forwarder
	closed    bool
	closeOnce sync.Once
}

func NewService(config Config) *Service {
	reconnect := config.Reconnect
	if reconnect.Attempts == 0 {
		reconnect.Attempts = 4
	}
	if reconnect.Attempts < 0 {
		reconnect.Attempts = 1
	}
	if reconnect.Backoff <= 0 {
		reconnect.Backoff = 500 * time.Millisecond
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = 15 * time.Second
	}
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = 10 * time.Second
	}
	if config.CloseTimeout <= 0 {
		config.CloseTimeout = 5 * time.Second
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		provider:         config.Provider,
		sessionHost:      config.SessionHost,
		rules:            config.Rules,
		isProduction:     config.IsProduction,
		env:              config.Policy.Environment(),
		reconnect:        reconnect,
		dialTimeout:      config.DialTimeout,
		handshakeTimeout: config.HandshakeTimeout,
		closeTimeout:     config.CloseTimeout,
		now:              config.Now,
		onError:          config.OnError,
		listen:           net.Listen,
		ctx:              ctx,
		cancel:           cancel,
		forwards:         make(map[string]*forwarder),
	}
}

func (s *Service) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

func (s *Service) Shutdown(context.Context) error {
	return s.Close()
}

func (s *Service) Environment() Environment {
	return s.env
}

func (s *Service) CreateLocal(ctx context.Context, args CreateLocalArgs) (Spec, error) {
	if err := s.available(); err != nil {
		return Spec{}, err
	}
	if strings.TrimSpace(args.SessionID) == "" {
		return Spec{}, ipc.BadParam(errors.New("会话 ID 不能为空"))
	}
	host := strings.TrimSpace(args.TargetHost)
	if host == "" {
		return Spec{}, ipc.BadParam(errors.New("目标主机不能为空"))
	}
	if args.TargetPort == 0 {
		return Spec{}, ipc.BadParam(errors.New("目标端口必须在 1-65535 之间"))
	}
	if _, err := s.validateSession(ctx, args.SessionID); err != nil {
		return Spec{}, err
	}
	hostID, err := s.hostFor(ctx, args.SessionID)
	if err != nil {
		return Spec{}, err
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	target := net.JoinHostPort(host, strconv.Itoa(int(args.TargetPort)))
	return s.create(ctx, args.SessionID, hostID, KindLocal, args.ListenPort, target, &host, &args.TargetPort)
}

func (s *Service) CreateSocks(ctx context.Context, args CreateSocksArgs) (Spec, error) {
	if err := s.available(); err != nil {
		return Spec{}, err
	}
	if strings.TrimSpace(args.SessionID) == "" {
		return Spec{}, ipc.BadParam(errors.New("会话 ID 不能为空"))
	}
	if s.env.exposed() && !args.AcknowledgeRisk {
		return Spec{}, ipc.NewError(ipc.CodeNeedsConfirm, "服务端 SOCKS5 是无认证代理，监听非回环地址前必须确认开放风险").WithDetail(ExposureRisk{
			Risk:           "unauthenticated_exposed_socks",
			ListenHost:     s.env.ListenHost,
			Authentication: "none",
		})
	}
	if _, err := s.validateSession(ctx, args.SessionID); err != nil {
		return Spec{}, err
	}
	hostID, err := s.hostFor(ctx, args.SessionID)
	if err != nil {
		return Spec{}, err
	}
	return s.create(ctx, args.SessionID, hostID, KindSOCKS, args.ListenPort, "", nil, nil)
}

func (s *Service) CreateRemote(ctx context.Context, args CreateRemoteArgs) (Spec, error) {
	if err := s.available(); err != nil {
		return Spec{}, err
	}
	if strings.TrimSpace(args.SessionID) == "" {
		return Spec{}, ipc.BadParam(errors.New("会话 ID 不能为空"))
	}
	host := strings.TrimSpace(args.TargetHost)
	if host == "" {
		return Spec{}, ipc.BadParam(errors.New("目标主机不能为空"))
	}
	if args.TargetPort == 0 {
		return Spec{}, ipc.BadParam(errors.New("目标端口必须在 1-65535 之间"))
	}
	bindHost := strings.TrimSpace(args.BindHost)
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	bindHost = strings.TrimPrefix(strings.TrimSuffix(bindHost, "]"), "[")
	if !isLoopbackHost(bindHost) && !args.AcknowledgeRisk {
		return Spec{}, ipc.NewError(ipc.CodeNeedsConfirm, "远程转发没有认证，远端监听非回环地址会把本地服务暴露给远端网络，必须确认开放风险").WithDetail(ExposureRisk{
			Risk:           "unauthenticated_exposed_remote_forward",
			ListenHost:     bindHost,
			Authentication: "none",
		})
	}
	dialer, err := s.validateSession(ctx, args.SessionID)
	if err != nil {
		return Spec{}, err
	}
	hostID, err := s.hostFor(ctx, args.SessionID)
	if err != nil {
		return Spec{}, err
	}
	if _, ok := dialer.(RemoteListener); !ok {
		return Spec{}, ipc.NewError(ipc.CodeUnsupported, "当前会话类型不支持远程监听")
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	target := net.JoinHostPort(host, strconv.Itoa(int(args.TargetPort)))
	return s.createRemote(ctx, dialer, args.SessionID, hostID, bindHost, args.BindPort, target, &host, &args.TargetPort)
}

func (s *Service) hostFor(ctx context.Context, sessionID string) (string, error) {
	if s.sessionHost == nil {
		return "", nil
	}
	hostID, err := s.sessionHost(ctx, sessionID)
	if err != nil {
		return "", session.IPCError(err)
	}
	return hostID, nil
}

func (s *Service) available() error {
	if s.env.Available {
		return nil
	}
	return ipc.NewError(ipc.CodeUnsupported, s.env.Reason)
}

func (s *Service) validateSession(ctx context.Context, sessionID string) (base.Dialer, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	if s.provider == nil {
		return nil, ipc.NewError(ipc.CodeUnsupported, "SSH 会话转发接口未配置")
	}
	dialer, err := s.currentDialer(ctx, sessionID)
	if err != nil {
		return nil, session.IPCError(err)
	}
	if dialer == nil {
		return nil, ipc.NewError(ipc.CodeUnsupported, "端口转发需要支持 SSH direct-tcpip 的会话")
	}
	return dialer, nil
}

func (s *Service) currentDialer(ctx context.Context, sessionID string) (base.Dialer, error) {
	if provider, ok := s.provider.(interface {
		CurrentDialerFor(context.Context, string) (base.Dialer, error)
	}); ok {
		return provider.CurrentDialerFor(ctx, sessionID)
	}
	return s.provider.CurrentDialer(ctx, sessionID)
}

func (s *Service) createRemote(ctx context.Context, dialer base.Dialer, sessionID, hostID, bindHost string, bindPort uint16, target string, targetHost *string, targetPort *uint16) (Spec, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Spec{}, ErrClosed
	}
	s.mu.Unlock()

	listener, err := dialer.(RemoteListener).ListenRemote(ctx, "tcp", net.JoinHostPort(bindHost, strconv.Itoa(int(bindPort))))
	if err != nil {
		return Spec{}, ipc.WrapError(ipc.CodeIO, "远端监听端口失败: "+err.Error(), err)
	}
	return s.start(ctx, sessionID, hostID, KindRemote, listener, bindHost, target, targetHost, targetPort)
}

func (s *Service) create(ctx context.Context, sessionID, hostID, kind string, listenPort uint16, target string, targetHost *string, targetPort *uint16) (Spec, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Spec{}, ErrClosed
	}
	s.mu.Unlock()

	address := net.JoinHostPort(s.env.ListenHost, strconv.Itoa(int(listenPort)))
	listener, err := s.listen("tcp", address)
	if err != nil {
		return Spec{}, ipc.WrapError(ipc.CodeIO, "监听转发端口失败: "+err.Error(), err)
	}
	return s.start(ctx, sessionID, hostID, kind, listener, s.env.ListenHost, target, targetHost, targetPort)
}

func (s *Service) start(requestCtx context.Context, sessionID, hostID, kind string, listener net.Listener, listenHost, target string, targetHost *string, targetPort *uint16) (Spec, error) {
	return s.startWithID(requestCtx, "", sessionID, hostID, kind, listener, listenHost, target, targetHost, targetPort)
}

func (s *Service) startWithID(requestCtx context.Context, id, sessionID, hostID, kind string, listener net.Listener, listenHost, target string, targetHost *string, targetPort *uint16) (Spec, error) {
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return Spec{}, ipc.WrapError(ipc.CodeIO, "解析转发监听地址失败: "+err.Error(), err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		_ = listener.Close()
		return Spec{}, ipc.WrapError(ipc.CodeIO, "转发监听端口无效", err)
	}

	ruleID := id
	if id == "" {
		id = ids.New()
	}
	spec := Spec{
		ID:         id,
		SessionID:  sessionID,
		HostID:     hostID,
		Status:     StatusActive,
		ListenHost: listenHost,
		ListenPort: uint16(port),
		TargetHost: targetHost,
		TargetPort: targetPort,
		Kind:       kind,
		CreatedAt:  s.now().UnixMilli(),
	}
	f := s.newForwarder(requestCtx, spec, ipc.OwnerID(requestCtx), target, listener)
	f.ruleID = ruleID

	f.wg.Add(1)
	s.mu.Lock()
	if s.closed || id == "" || s.forwards[id] != nil {
		s.mu.Unlock()
		f.wg.Done()
		f.initiateStop()
		return Spec{}, ipc.NewError(ipc.CodeInternal, "转发 ID 冲突或服务已关闭")
	}
	s.forwards[id] = f
	s.mu.Unlock()

	go f.acceptLoop()
	if _, err := s.validateSession(requestCtx, sessionID); err != nil {
		s.discard(f)
		return Spec{}, err
	}
	s.mu.Lock()
	current := s.forwards[id] == f && f.spec.Status == StatusActive
	s.mu.Unlock()
	if !current {
		s.discard(f)
		return Spec{}, ipc.NewError(ipc.CodeIO, "会话已断开，转发监听未启动")
	}
	return spec, nil
}

func (s *Service) discard(f *forwarder) {
	s.mu.Lock()
	if s.forwards[f.id] == f {
		delete(s.forwards, f.id)
	}
	s.mu.Unlock()
	if err := f.stopAndWait(); err != nil {
		s.report(fmt.Errorf("转发 %s 监听关闭失败: %w", f.id, err))
	}
}

func (s *Service) newForwarder(requestCtx context.Context, spec Spec, owner, target string, listener net.Listener) *forwarder {
	ctx, cancel := context.WithCancel(context.WithoutCancel(requestCtx))
	stopServiceCancel := context.AfterFunc(s.ctx, cancel)
	return &forwarder{
		id: spec.ID, sessionID: spec.SessionID, owner: owner, kind: spec.Kind, target: target,
		service: s, listener: listener, ctx: ctx,
		cancel: func() { stopServiceCancel(); cancel() },
		conns:  make(map[net.Conn]struct{}), spec: spec,
	}
}

func (s *Service) List() []Spec {
	return s.list("")
}

func (s *Service) ListFor(ctx context.Context) []Spec {
	return s.list(ipc.OwnerID(ctx))
}

func (s *Service) list(owner string) []Spec {
	s.mu.Lock()
	result := make([]Spec, 0, len(s.forwards))
	for _, f := range s.forwards {
		if owner == "" || f.owner == owner {
			result = append(result, f.spec)
		}
	}
	s.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Service) Remove(id string) error {
	if ruleID, ok := s.savedRuntime(id); ok {
		_, err := s.StopRule(context.Background(), ruleID)
		return err
	}
	return s.remove("", id)
}

func (s *Service) RemoveFor(ctx context.Context, id string) error {
	if ruleID, ok := s.savedRuntime(id); ok {
		_, err := s.StopRule(ctx, ruleID)
		return err
	}
	return s.remove(ipc.OwnerID(ctx), id)
}

func (s *Service) RetryFor(ctx context.Context, id, sessionID string) (Spec, error) {
	if ruleID, ok := s.savedRuntime(id); ok {
		if sessionID == "" {
			s.mu.Lock()
			if f := s.forwards[ruleID]; f != nil {
				sessionID = f.spec.SessionID
			}
			s.mu.Unlock()
		}
		if _, err := s.StartRule(ctx, ruleID, sessionID); err != nil {
			return Spec{}, err
		}
		spec, ok := s.runtimeForRule(ctx, ruleID)
		if !ok {
			return Spec{}, ipc.NewError(ipc.CodeNotFound, "转发规则运行时不存在或已移除")
		}
		return spec, nil
	}
	return s.retry(ctx, ipc.OwnerID(ctx), id, sessionID)
}

func (s *Service) retry(ctx context.Context, owner, id, requestedSessionID string) (Spec, error) {
	s.mu.Lock()
	previous := s.forwards[id]
	if previous == nil {
		s.mu.Unlock()
		return Spec{}, ipc.NewError(ipc.CodeNotFound, "转发不存在或已移除")
	}
	if owner != "" && previous.owner != owner {
		s.mu.Unlock()
		return Spec{}, session.IPCError(session.ErrForbidden)
	}
	oldSpec := previous.spec
	if requestedSessionID == "" {
		requestedSessionID = oldSpec.SessionID
	}
	if previous.retrying {
		s.mu.Unlock()
		return Spec{}, ipc.NewError(ipc.CodeBadParam, "转发正在重连，请稍候")
	}
	if oldSpec.Status == StatusActive && requestedSessionID == oldSpec.SessionID {
		s.mu.Unlock()
		return oldSpec, nil
	}
	s.mu.Unlock()
	dialer, err := s.validateSession(ctx, requestedSessionID)
	if err != nil {
		if oldSpec.Status != StatusActive && requestedSessionID == oldSpec.SessionID {
			s.setFailed(previous, "转发重新连接失败: "+err.Error())
		}
		return Spec{}, err
	}
	hostID, err := s.hostFor(ctx, requestedSessionID)
	if err != nil {
		if oldSpec.Status != StatusActive && requestedSessionID == oldSpec.SessionID {
			s.setFailed(previous, "转发重新连接失败: "+err.Error())
		}
		return Spec{}, err
	}
	if oldSpec.HostID != hostID {
		return Spec{}, session.IPCError(session.ErrForbidden)
	}
	if oldSpec.Kind == KindRemote {
		if _, ok := dialer.(RemoteListener); !ok {
			return Spec{}, ipc.NewError(ipc.CodeUnsupported, "当前会话类型不支持远程监听")
		}
	}
	s.mu.Lock()
	if s.closed || s.forwards[id] != previous || previous.retrying {
		s.mu.Unlock()
		return Spec{}, ipc.NewError(ipc.CodeNotFound, "转发已改变，请刷新后重试")
	}
	previous.retrying = true
	previous.spec.Status = StatusReconnecting
	previous.spec.Error = ""
	s.mu.Unlock()
	if err := previous.stopAndWait(); err != nil {
		s.report(fmt.Errorf("转发 %s 旧监听关闭失败: %w", id, err))
	}
	address := net.JoinHostPort(oldSpec.ListenHost, strconv.Itoa(int(oldSpec.ListenPort)))
	var listener net.Listener
	if oldSpec.Kind == KindRemote {
		listener, err = dialer.(RemoteListener).ListenRemote(ctx, "tcp", address)
	} else {
		listener, err = s.listen("tcp", address)
	}
	if err != nil {
		s.setFailed(previous, "重新监听转发端口失败: "+err.Error())
		return Spec{}, ipc.WrapError(ipc.CodeIO, "重新监听转发端口失败: "+err.Error(), err)
	}
	spec := oldSpec
	spec.SessionID = requestedSessionID
	spec.Status = StatusActive
	spec.Error = ""
	replacement := s.newForwarder(ctx, spec, previous.owner, previous.target, listener)
	replacement.ruleID = previous.ruleID
	replacement.wg.Add(1)
	s.mu.Lock()
	if s.closed || s.forwards[id] != previous || previous.spec.Status != StatusReconnecting {
		s.mu.Unlock()
		replacement.wg.Done()
		replacement.initiateStop()
		return Spec{}, ipc.NewError(ipc.CodeIO, "转发重新连接期间会话已断开或转发已移除")
	}
	s.forwards[id] = replacement
	s.mu.Unlock()
	go replacement.acceptLoop()
	if _, err := s.validateSession(ctx, requestedSessionID); err != nil {
		s.setFailed(replacement, "会话已断开，转发重新连接未完成: "+err.Error())
		if closeErr := replacement.stopAndWait(); closeErr != nil {
			s.report(fmt.Errorf("转发 %s 监听关闭失败: %w", id, closeErr))
		}
		return Spec{}, err
	}
	s.mu.Lock()
	active := s.forwards[id] == replacement && replacement.spec.Status == StatusActive
	s.mu.Unlock()
	if !active {
		return Spec{}, ipc.NewError(ipc.CodeIO, "转发重新连接期间会话已断开或监听失败")
	}
	return spec, nil
}

func (s *Service) setFailed(f *forwarder, message string) {
	s.mu.Lock()
	if s.forwards[f.id] == f {
		f.spec.Status = StatusFailed
		f.spec.Error = message
		f.retrying = false
	}
	s.mu.Unlock()
}

func (s *Service) SuspendRemote(sessionID string) {
	s.mu.Lock()
	forwards := make([]*forwarder, 0)
	for _, f := range s.forwards {
		if f.sessionID == sessionID && f.kind == KindRemote && f.spec.Status == StatusActive {
			f.spec.Status = StatusReconnecting
			f.spec.Error = ""
			forwards = append(forwards, f)
		}
	}
	s.mu.Unlock()
	for _, f := range forwards {
		if err := f.stopAndWait(); err != nil {
			s.report(fmt.Errorf("转发 %s 旧监听关闭失败: %w", f.id, err))
		}
	}
}

func (s *Service) RestoreRemote(ctx context.Context, sessionID string) {
	s.mu.Lock()
	ids := make([]string, 0)
	for id, f := range s.forwards {
		if f.sessionID == sessionID && f.kind == KindRemote && f.spec.Status == StatusReconnecting {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		if _, err := s.retry(ctx, "", id, sessionID); err != nil {
			s.report(fmt.Errorf("远程转发 %s 自动重新连接失败: %w", id, err))
		}
	}
}

func (s *Service) FailSession(sessionID, reason string) {
	s.mu.Lock()
	forwards := make([]*forwarder, 0)
	for _, f := range s.forwards {
		if f.sessionID == sessionID {
			f.spec.Status = StatusFailed
			f.spec.Error = reason
			forwards = append(forwards, f)
		}
	}
	s.mu.Unlock()
	for _, f := range forwards {
		if err := f.stopAndWait(); err != nil {
			s.report(fmt.Errorf("转发 %s 监听关闭失败: %w", f.id, err))
		}
	}
}

func (s *Service) remove(owner, id string) error {
	s.mu.Lock()
	f := s.forwards[id]
	if f != nil && owner != "" && f.owner != owner {
		s.mu.Unlock()
		return session.IPCError(session.ErrForbidden)
	}
	delete(s.forwards, id)
	s.mu.Unlock()
	if f == nil {
		return nil
	}
	if err := f.stopAndWait(); err != nil {
		return ipc.WrapError(ipc.CodeIO, "转发监听关闭失败（远端端口可能仍在监听）: "+err.Error(), err)
	}
	return nil
}

func (s *Service) CloseSession(sessionID string) {
	s.mu.Lock()
	forwards := make([]*forwarder, 0, len(s.forwards))
	for id, f := range s.forwards {
		if f.sessionID == sessionID {
			forwards = append(forwards, f)
			delete(s.forwards, id)
		}
	}
	s.mu.Unlock()
	for _, f := range forwards {
		if err := f.stopAndWait(); err != nil {
			s.report(fmt.Errorf("转发 %s 监听关闭失败: %w", f.id, err))
		}
	}
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		forwards := make([]*forwarder, 0, len(s.forwards))
		for id, f := range s.forwards {
			forwards = append(forwards, f)
			delete(s.forwards, id)
		}
		s.mu.Unlock()
		s.cancel()
		for _, f := range forwards {
			if err := f.stopAndWait(); err != nil {
				s.report(fmt.Errorf("转发 %s 监听关闭失败: %w", f.id, err))
			}
		}
	})
	return nil
}

func (s *Service) report(err error) {
	if err != nil && s.onError != nil {
		s.onError(err)
	}
}

func (s *Service) openUpstream(ctx context.Context, sessionID, target string) (net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < s.reconnect.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dialer, err := s.currentDialer(ctx, sessionID)
		if err == nil && dialer == nil {
			err = errors.New("current session has no SSH dialer")
		}
		var conn net.Conn
		if err == nil {
			dialCtx, cancel := context.WithTimeout(ctx, s.dialTimeout)
			conn, err = dialer.DialContext(dialCtx, "tcp", target)
			cancel()
		}
		if err == nil && conn == nil {
			err = errors.New("SSH dialer returned a nil connection")
		}
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if attempt+1 < s.reconnect.Attempts {
			timer := time.NewTimer(s.reconnect.Backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, fmt.Errorf("SSH 转发连接 %s 失败: %w", target, lastErr)
}

type forwarder struct {
	id        string
	ruleID    string
	sessionID string
	owner     string
	kind      string
	target    string
	service   *Service
	spec      Spec
	listener  net.Listener
	ctx       context.Context
	cancel    context.CancelFunc

	stopOnce sync.Once
	retrying bool
	wg       sync.WaitGroup
	mu       sync.Mutex
	stopped  bool
	closeErr error
	conns    map[net.Conn]struct{}
}

func (f *forwarder) acceptLoop() {
	defer f.wg.Done()
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			if f.ctx.Err() != nil {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Temporary() {
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-f.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				continue
			}
			f.service.setFailed(f, "转发监听失败: "+err.Error())
			f.initiateStop()
			f.service.report(fmt.Errorf("转发 %s 监听失败: %w", f.id, err))
			return
		}
		if !f.track(conn) {
			_ = conn.Close()
			continue
		}
		f.wg.Add(1)
		go f.handle(conn)
	}
}

func (f *forwarder) handle(client net.Conn) {
	defer f.wg.Done()
	defer func() {
		f.untrack(client)
		_ = client.Close()
	}()
	var err error
	switch f.kind {
	case KindLocal:
		var upstream net.Conn
		upstream, err = f.service.openUpstream(f.ctx, f.sessionID, f.target)
		if err == nil {
			err = f.relayTracked(client, upstream)
		}
	case KindSOCKS:
		err = f.service.serveSOCKS(f, client)
	case KindRemote:
		var upstream net.Conn
		upstream, err = f.dialLocal()
		if err == nil {
			err = f.relayTracked(client, upstream)
		}
	}
	if err != nil && f.ctx.Err() == nil {
		f.service.report(fmt.Errorf("转发 %s 连接失败: %w", f.id, err))
	}
}

func (f *forwarder) dialLocal() (net.Conn, error) {
	dialer := &net.Dialer{Timeout: f.service.dialTimeout}
	conn, err := dialer.DialContext(f.ctx, "tcp", f.target)
	if err != nil {
		return nil, fmt.Errorf("连接本地目标 %s 失败: %w", f.target, err)
	}
	return conn, nil
}

func (f *forwarder) relayTracked(client, upstream net.Conn) error {
	if !f.track(upstream) {
		_ = upstream.Close()
		return f.ctx.Err()
	}
	defer func() {
		f.untrack(upstream)
		_ = upstream.Close()
	}()
	relay(client, upstream)
	return nil
}

func (f *forwarder) track(conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return false
	}
	f.conns[conn] = struct{}{}
	return true
}

func (f *forwarder) untrack(conn net.Conn) {
	f.mu.Lock()
	delete(f.conns, conn)
	f.mu.Unlock()
}

func (f *forwarder) initiateStop() {
	f.stopOnce.Do(func() {
		f.mu.Lock()
		f.stopped = true
		connections := make([]net.Conn, 0, len(f.conns))
		for conn := range f.conns {
			connections = append(connections, conn)
		}
		f.mu.Unlock()
		f.cancel()
		closed := make(chan error, 1)
		go func() { closed <- f.listener.Close() }()
		for _, conn := range connections {
			_ = conn.Close()
		}
		select {
		case err := <-closed:
			f.closeErr = err
		case <-time.After(f.service.closeTimeout):
			f.closeErr = errListenerCloseTimeout
			if aborter, ok := f.listener.(interface{ AbortClose() }); ok {
				aborter.AbortClose()
				<-closed
			}
		}
	})
}

func (f *forwarder) stopAndWait() error {
	f.initiateStop()
	f.wg.Wait()
	return f.closeErr
}

func relay(left, right net.Conn) {
	setNoDelay(left)
	setNoDelay(right)
	done := make(chan struct{}, 2)
	copyStream := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if closeWriter, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = closeWriter.CloseWrite()
		} else {
			_ = dst.Close()
		}
		if err != nil {
			_ = dst.Close()
			_ = src.Close()
		}
		done <- struct{}{}
	}
	go copyStream(left, right)
	go copyStream(right, left)
	<-done
	<-done
}

func setNoDelay(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
}
