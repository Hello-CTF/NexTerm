package wol

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/wol/netrule"
)

const (
	RouteLocal  = "local"
	RouteDevice = "device"

	AuditKind = "wake_asset"

	// cooldown stops a page from turning the command into a packet generator.
	cooldown = 3 * time.Second
)

// Asset is the wake-time view of a stored asset.
type Asset struct {
	ID          string
	OwnerID     string
	Kind        string
	Name        string
	OptionsJSON string
}

// AssetResolver reads the stored asset at call time; options are never taken from a session snapshot.
type AssetResolver interface {
	ResolveAsset(ctx context.Context, id string) (Asset, error)
}

type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// DeviceDialerFactory returns a dialer through the connected session of device/<id>.
type DeviceDialerFactory func(ctx context.Context, viaAssetID string) (DialFunc, error)

type AuditEvent struct {
	Kind    string
	AssetID string
	Payload map[string]any
}

type Auditor interface {
	Audit(ctx context.Context, event AuditEvent) error
}

// LocalSender sends one datagram from this machine.
type LocalSender func(ctx context.Context, target string, packet []byte) error

type Option func(*Service)

func WithDeviceDialer(factory DeviceDialerFactory) Option {
	return func(s *Service) { s.device = factory }
}
func WithAuditor(auditor Auditor) Option      { return func(s *Service) { s.auditor = auditor } }
func WithLocalSender(send LocalSender) Option { return func(s *Service) { s.local = send } }
func WithClock(now func() time.Time) Option   { return func(s *Service) { s.now = now } }

type Service struct {
	resolver AssetResolver
	device   DeviceDialerFactory
	auditor  Auditor
	local    LocalSender
	now      func() time.Time
	logger   *slog.Logger

	mu      sync.Mutex
	last    map[string]time.Time
	buckets map[string]*tokenBucket
}

func NewService(resolver AssetResolver, options ...Option) *Service {
	s := &Service{resolver: resolver, local: SendUDP, now: time.Now, logger: slog.Default(), last: map[string]time.Time{}, buckets: map[string]*tokenBucket{}}
	for _, option := range options {
		option(s)
	}
	return s
}

type WakeAssetRequest struct {
	AssetID string `json:"assetId"`
}

// WakeAssetResult says what was sent and from where. A sent packet does not mean the host woke.
type WakeAssetResult struct {
	AssetID  string `json:"assetId"`
	Route    string `json:"route"` // "local" or "device"
	ViaID    string `json:"viaId,omitempty"`
	Target   string `json:"target"`
	Mac      string `json:"mac"`
	Bytes    int    `json:"bytes"`
	SentAtMs int64  `json:"sentAtMs"`
	// Note explains a route choice the user may not expect, such as an SSH jump asset being ignored.
	Note string `json:"note,omitempty"`
}

var networkKinds = map[string]bool{"ssh": true, "docker": true, "winrm": true, "rdp": true, "vnc": true}

// Wake validates ownership, reads the stored options and sends a magic packet. Every outcome after the asset is
// resolved is audited; a failed send is returned as an error and never reported as sent.
func (s *Service) Wake(ctx context.Context, assetID string) (WakeAssetResult, error) {
	if err := ipc.CheckAccountSession(ctx); err != nil {
		return WakeAssetResult{}, err
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return WakeAssetResult{}, ipc.NewError(ipc.CodeBadParam, "资产 ID 不能为空")
	}
	if s.resolver == nil {
		return WakeAssetResult{}, ipc.NewError(ipc.CodeUnsupported, "唤醒服务未配置")
	}
	asset, err := s.resolver.ResolveAsset(ctx, assetID)
	if err != nil {
		return WakeAssetResult{}, err
	}
	if owner := ipc.OwnerID(ctx); owner != "" && asset.OwnerID != owner {
		return WakeAssetResult{}, ipc.NewError(ipc.CodeForbidden, "无权访问该资产")
	}
	if !networkKinds[asset.Kind] {
		return WakeAssetResult{}, ipc.NewError(ipc.CodeBadParam, "该类型的资产不支持网络唤醒")
	}
	options, err := ParseOptions(asset.OptionsJSON)
	if err != nil {
		return WakeAssetResult{}, err
	}
	if options.WakeMac == "" {
		return WakeAssetResult{}, ipc.NewError(ipc.CodeBadParam, "该资产没有设置唤醒 MAC 地址，请在资产设置中填写")
	}
	mac, err := ParseMAC(options.WakeMac)
	if err != nil {
		return WakeAssetResult{}, err
	}
	destination, err := options.Destination()
	if err != nil {
		return WakeAssetResult{}, err
	}
	packet, err := MagicPacket(mac)
	if err != nil {
		return WakeAssetResult{}, err
	}
	result := WakeAssetResult{AssetID: asset.ID, Route: RouteLocal, Target: destination.String(), Mac: FormatMAC(mac), Bytes: len(packet)}
	if strings.HasPrefix(options.ViaAssetID, DevicePrefix) {
		result.Route, result.ViaID = RouteDevice, options.ViaAssetID
	} else if options.ViaAssetID != "" {
		result.Note = "该资产经 SSH 跳板连接，唤醒包仍从运行 NexTerm 的这台机器发出，不经过跳板"
	}
	if err := checkRoleTarget(ctx, destination.Addr()); err != nil {
		s.audit(ctx, asset.ID, "refused", result, err)
		return WakeAssetResult{}, err
	}
	if err := s.allow(ctx, asset.ID); err != nil {
		s.audit(ctx, asset.ID, "refused", result, err)
		return WakeAssetResult{}, err
	}
	// The intent is recorded before anything leaves the machine; without a record the packet is not sent.
	if err := s.auditStrict(ctx, asset.ID, "intent", result, nil); err != nil {
		return WakeAssetResult{}, ipc.NewError(ipc.CodeInternal, "无法写入审计记录，未发送唤醒包: "+err.Error())
	}
	if err := s.send(ctx, result, destination, packet); err != nil {
		s.audit(ctx, asset.ID, "result", result, err)
		return WakeAssetResult{}, err
	}
	result.SentAtMs = s.now().UnixMilli()
	if err := s.auditStrict(ctx, asset.ID, "result", result, nil); err != nil {
		s.logger.Warn("wake audit failed", "error", err)
		result.Note = strings.TrimSpace(result.Note + " 唤醒包已发出，但结果审计记录写入失败")
	}
	return result, nil
}

// checkRoleTarget applies the role part of the target rule at send time. Desktop mode (no owner) may use any address
// that is not always denied; in server mode only the limited broadcast and RFC 1918 addresses are allowed unless the
// caller is a superadmin.
func checkRoleTarget(ctx context.Context, addr netip.Addr) error {
	if ipc.OwnerID(ctx) == "" || netrule.Private(addr) {
		return nil
	}
	if role, ok := ipc.RoleFromContext(ctx); ok && role == "superadmin" {
		return nil
	}
	return ipc.NewError(ipc.CodeForbidden, "服务器模式下，唤醒目标只能是 255.255.255.255 或内网地址（10/8、172.16/12、192.168/16）")
}

// allow enforces the per-asset cooldown and a per-owner token bucket across all assets. Both maps are pruned on use
// and the bucket map is hard-capped, so memory stays bounded.
func (s *Service) allow(ctx context.Context, assetID string) error {
	owner := ipc.OwnerID(ctx)
	key := owner + "\x00" + assetID
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.last[key]; ok && now.Sub(at) < cooldown {
		return ipc.NewError(ipc.CodeBadParam, "唤醒包刚刚发送过，请几秒后再试")
	}
	for k, at := range s.last {
		if now.Sub(at) >= cooldown {
			delete(s.last, k)
		}
	}
	bucket, ok := s.buckets[owner]
	if !ok {
		if len(s.buckets) >= maxBuckets {
			for k, b := range s.buckets {
				if b.refilled(now) >= bucketBurst {
					delete(s.buckets, k)
				}
			}
			if len(s.buckets) >= maxBuckets {
				return ipc.NewError(ipc.CodeBadParam, "唤醒请求过于频繁，请稍后再试")
			}
		}
		bucket = &tokenBucket{tokens: bucketBurst, at: now}
		s.buckets[owner] = bucket
	}
	if !bucket.take(now) {
		return ipc.NewError(ipc.CodeBadParam, "唤醒请求过于频繁，请稍后再试")
	}
	s.last[key] = now
	return nil
}

const (
	bucketBurst    = 10.0
	bucketInterval = 6 * time.Second // one token per interval
	maxBuckets     = 4096
)

type tokenBucket struct {
	tokens float64
	at     time.Time
}

func (b *tokenBucket) refilled(now time.Time) float64 {
	tokens := b.tokens + float64(now.Sub(b.at))/float64(bucketInterval)
	if tokens > bucketBurst {
		return bucketBurst
	}
	return tokens
}

func (b *tokenBucket) take(now time.Time) bool {
	tokens := b.refilled(now)
	b.at = now
	if tokens < 1 {
		b.tokens = tokens
		return false
	}
	b.tokens = tokens - 1
	return true
}

func (s *Service) send(ctx context.Context, result WakeAssetResult, destination netip.AddrPort, packet []byte) error {
	if result.Route == RouteLocal {
		if err := s.local(ctx, destination.String(), packet); err != nil {
			return ipc.NewError(ipc.CodeIO, "本机发送唤醒包失败: "+err.Error())
		}
		return nil
	}
	if s.device == nil {
		return ipc.NewError(ipc.CodeUnsupported, "设备通道未配置，无法通过设备发送唤醒包")
	}
	dial, err := s.device(ctx, result.ViaID)
	if err != nil {
		return err
	}
	return sendViaDevice(ctx, dial, destination.String(), packet)
}

// sendViaDevice dials udp4 through the device agent and waits for the agent to close the datagram socket, which
// it does only after writing. An agent that predates UDP forwarding rejects the network and is reported as such.
func sendViaDevice(ctx context.Context, dial DialFunc, target string, packet []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := dial(ctx, "udp4", target)
	if err != nil {
		if errors.Is(err, deviceproto.ErrInvalid) {
			// An agent without UDP forwarding names the network; a current agent that refuses the target does not.
			if strings.Contains(err.Error(), "unsupported dial network") {
				return ipc.NewError(ipc.CodeUnsupported, "该设备的 Agent 版本过旧，不支持发送唤醒包，请升级 Agent 后重试")
			}
			return ipc.NewError(ipc.CodeForbidden, "设备拒绝发送唤醒包: "+err.Error())
		}
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(packet); err != nil {
		return ipc.NewError(ipc.CodeIO, "向设备写入唤醒包失败: "+err.Error())
	}
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return ipc.NewError(ipc.CodeTimeout, "设备没有确认已发出唤醒包，结果未知")
		}
		return ipc.NewError(ipc.CodeIO, "设备拒绝或中断了唤醒包: "+err.Error())
	}
	return nil
}

func (s *Service) audit(ctx context.Context, assetID, phase string, result WakeAssetResult, sendErr error) {
	if err := s.auditStrict(ctx, assetID, phase, result, sendErr); err != nil {
		s.logger.Warn("wake audit failed", "phase", phase, "error", err)
	}
}

func (s *Service) auditStrict(ctx context.Context, assetID, phase string, result WakeAssetResult, sendErr error) error {
	if s.auditor == nil {
		return nil
	}
	payload := map[string]any{"phase": phase, "mac": result.Mac, "target": result.Target, "route": result.Route, "viaAssetId": result.ViaID, "sent": phase == "result" && sendErr == nil}
	if sendErr != nil {
		payload["error"] = sendErr.Error()
	}
	return s.auditor.Audit(context.WithoutCancel(ctx), AuditEvent{Kind: AuditKind, AssetID: assetID, Payload: payload})
}

// RegisterCommands registers wake_asset.
func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	return ipc.Register(dispatcher, "wake_asset", func(ctx context.Context, _ *ipc.Call, input WakeAssetRequest) (WakeAssetResult, error) {
		return s.Wake(ctx, input.AssetID)
	})
}
