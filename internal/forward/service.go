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

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type Service struct {
	provider         DialerProvider
	env              Environment
	reconnect        ReconnectPolicy
	dialTimeout      time.Duration
	handshakeTimeout time.Duration
	newID            func() string
	now              func() time.Time
	onError          func(error)
	listen           func(string, string) (net.Listener, error)

	ctx       context.Context
	cancel    context.CancelFunc
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
	if config.NewID == nil {
		config.NewID = ids.New
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		provider:         config.Provider,
		env:              config.Policy.Environment(),
		reconnect:        reconnect,
		dialTimeout:      config.DialTimeout,
		handshakeTimeout: config.HandshakeTimeout,
		newID:            config.NewID,
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
		return Spec{}, ipc.BadParam(errors.New("sessionId 不能为空"))
	}
	host := strings.TrimSpace(args.TargetHost)
	if host == "" {
		return Spec{}, ipc.BadParam(errors.New("目标主机不能为空"))
	}
	if args.TargetPort == 0 {
		return Spec{}, ipc.BadParam(errors.New("目标端口必须在 1–65535 之间"))
	}
	if err := s.validateSession(ctx, args.SessionID); err != nil {
		return Spec{}, err
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	target := net.JoinHostPort(host, strconv.Itoa(int(args.TargetPort)))
	return s.create(ctx, args.SessionID, KindLocal, args.ListenPort, target, &host, &args.TargetPort)
}

func (s *Service) CreateSocks(ctx context.Context, args CreateSocksArgs) (Spec, error) {
	if err := s.available(); err != nil {
		return Spec{}, err
	}
	if strings.TrimSpace(args.SessionID) == "" {
		return Spec{}, ipc.BadParam(errors.New("sessionId 不能为空"))
	}
	if s.env.exposed() && !args.AcknowledgeRisk {
		return Spec{}, ipc.NewError(ipc.CodeNeedsConfirm, "服务端 SOCKS5 是无认证代理，监听非回环地址前必须确认开放风险").WithDetail(ExposureRisk{
			Risk:           "unauthenticated_exposed_socks",
			ListenHost:     s.env.ListenHost,
			Authentication: "none",
		})
	}
	if err := s.validateSession(ctx, args.SessionID); err != nil {
		return Spec{}, err
	}
	return s.create(ctx, args.SessionID, KindSOCKS, args.ListenPort, "", nil, nil)
}

func (s *Service) available() error {
	if s.env.Available {
		return nil
	}
	return ipc.NewError(ipc.CodeUnsupported, s.env.Reason)
}

func (s *Service) validateSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if s.provider == nil {
		return ipc.NewError(ipc.CodeUnsupported, "SSH 会话转发接口未配置")
	}
	dialer, err := s.provider.CurrentDialer(ctx, sessionID)
	if err != nil {
		return err
	}
	if dialer == nil {
		return ipc.NewError(ipc.CodeUnsupported, "端口转发需要支持 SSH direct-tcpip 的会话")
	}
	return nil
}

func (s *Service) create(_ context.Context, sessionID, kind string, listenPort uint16, target string, targetHost *string, targetPort *uint16) (Spec, error) {
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

	id := s.newID()
	ctx, cancel := context.WithCancel(s.ctx)
	f := &forwarder{
		id:        id,
		sessionID: sessionID,
		kind:      kind,
		target:    target,
		service:   s,
		listener:  listener,
		ctx:       ctx,
		cancel:    cancel,
		conns:     make(map[net.Conn]struct{}),
	}
	f.spec = Spec{
		ID:         id,
		SessionID:  sessionID,
		ListenHost: s.env.ListenHost,
		ListenPort: uint16(port),
		TargetHost: targetHost,
		TargetPort: targetPort,
		Kind:       kind,
		CreatedAt:  s.now().UnixMilli(),
	}

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
	return f.spec, nil
}

func (s *Service) List() []Spec {
	s.mu.Lock()
	result := make([]Spec, 0, len(s.forwards))
	for _, f := range s.forwards {
		result = append(result, f.spec)
	}
	s.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Service) Remove(id string) error {
	s.mu.Lock()
	f := s.forwards[id]
	delete(s.forwards, id)
	s.mu.Unlock()
	if f == nil {
		return nil
	}
	f.stopAndWait()
	return nil
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
			f.stopAndWait()
		}
	})
	return nil
}

func (s *Service) evict(f *forwarder) {
	s.mu.Lock()
	if s.forwards[f.id] == f {
		delete(s.forwards, f.id)
	}
	s.mu.Unlock()
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
		dialer, err := s.provider.CurrentDialer(ctx, sessionID)
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
	sessionID string
	kind      string
	target    string
	service   *Service
	spec      Spec
	listener  net.Listener
	ctx       context.Context
	cancel    context.CancelFunc

	stopOnce sync.Once
	wg       sync.WaitGroup
	mu       sync.Mutex
	stopped  bool
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
			f.service.evict(f)
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
	}
	if err != nil && f.ctx.Err() == nil {
		f.service.report(fmt.Errorf("转发 %s 连接失败: %w", f.id, err))
	}
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
		_ = f.listener.Close()
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
}

func (f *forwarder) stopAndWait() {
	f.initiateStop()
	f.wg.Wait()
}

func relay(left, right net.Conn) {
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
