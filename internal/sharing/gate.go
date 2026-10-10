package sharing

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

var (
	// ErrInputNotAllowed 表示只读授权的输入方向被拒绝。
	ErrInputNotAllowed = errors.New("sharing: grant is read-only")
	// ErrGrantExpired 表示授权已过有效期, 双向都必须停止。
	ErrGrantExpired = errors.New("sharing: grant expired")
)

// RecheckFunc 让执行路径在会话中途按当前数据库状态刷新授权 (例如调用
// Service.RevalidateLink / Service.RevalidateHostAccess); 返回的 Grant 替换
// 快照并立即作用于后续判定 (权限收缩即时生效), 返回错误即双向停止。
type RecheckFunc func(ctx context.Context, grant *Grant) (*Grant, error)

// Gate 是分享流的执行点: 输出方向 (终端到 viewer) 始终放行, 输入方向仅对
// read_write 授权放行; 每个数据块在 Read 前与 Write 前各复查一次 (可用
// WithRevalidateTTL 把重查收敛为短 TTL 缓存), 授权失效后双向立即停止,
// 失效或取消期间读到的块一律丢弃。阻塞的 Read 由集成层在取消时关闭 source 唤醒。
type Gate struct {
	refreshMu  sync.Mutex
	mu         sync.Mutex
	grant      *Grant
	now        func() int64
	recheck    RecheckFunc
	inputCheck func(ctx context.Context, grant *Grant) (*Grant, error)

	revalidateTTL time.Duration
	recheckCache  refreshCache
	inputCache    refreshCache
}

// refreshCache 是 revalidate 短 TTL 缓存的条目: grant 是上次成功刷新的快照,
// at 是其刷新时刻 (毫秒)。
type refreshCache struct {
	grant *Grant
	at    int64
}

type GateOption func(*Gate)

func WithGateNow(now func() int64) GateOption {
	return func(g *Gate) {
		if now != nil {
			g.now = now
		}
	}
}

func WithRecheck(recheck RecheckFunc) GateOption {
	return func(g *Gate) {
		g.recheck = recheck
	}
}

// WithInputCheck 为输入方向附加 Write 前的逐块强制点 (如公开链接的审计输入
// 检查); 返回的 Grant 同样替换快照。
func WithInputCheck(check func(ctx context.Context, grant *Grant) (*Grant, error)) GateOption {
	return func(g *Gate) {
		g.inputCheck = check
	}
}

// WithRevalidateTTL 打开 recheck 与 inputCheck 的短 TTL 缓存: TTL 内的重复
// 调用不重查, 沿用上次的刷新快照, 吊销/收缩最迟在 TTL 后的下一次调用生效
// (空闲连接由集成层的周期 Check 兜住)。非正数 (默认) 关闭缓存, 逐块重查。
func WithRevalidateTTL(ttl time.Duration) GateOption {
	return func(g *Gate) {
		g.revalidateTTL = ttl
	}
}

func NewGate(grant *Grant, options ...GateOption) *Gate {
	g := &Gate{grant: grant, now: ids.NowMS}
	for _, option := range options {
		option(g)
	}
	return g
}

func (g *Gate) Grant() *Grant {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.grant == nil {
		return nil
	}
	copied := *g.grant
	return &copied
}

func (g *Gate) setGrant(grant *Grant) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grant = grant
}

func (g *Gate) InputAllowed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.grant != nil && g.grant.Permission.AllowsWrite()
}

// Check 先按 recheck 刷新授权快照 (支持权限收缩与有效期变化), 再按当前
// ExpiresAt 判定; 过期或 recheck 失败 (吊销) 都返回错误。
func (g *Gate) Check(ctx context.Context) error {
	return g.check(ctx, func(ctx context.Context, grant *Grant) (*Grant, error) {
		return g.cachedRefresh(ctx, grant, g.recheck, &g.recheckCache)
	})
}

// CheckFresh 与 Check 判定相同, 但绕过 TTL 缓存强制重查: 集成层在数据通路
// 已经断开时用它取当前授权状态, 缓存窗口不会掩盖刚刚发生的过期/吊销。
func (g *Gate) CheckFresh(ctx context.Context) error {
	return g.check(ctx, func(ctx context.Context, grant *Grant) (*Grant, error) {
		if g.recheck == nil {
			return nil, nil
		}
		return g.recheck(ctx, grant)
	})
}

func (g *Gate) check(ctx context.Context, refresh func(ctx context.Context, grant *Grant) (*Grant, error)) error {
	g.refreshMu.Lock()
	defer g.refreshMu.Unlock()
	grant := g.Grant()
	if grant == nil {
		return errors.New("sharing: nil grant")
	}
	refreshed, err := refresh(ctx, grant)
	if err != nil {
		return err
	}
	if refreshed != nil {
		g.setGrant(refreshed)
	}
	if g.now() >= g.Grant().ExpiresAt {
		return ErrGrantExpired
	}
	return nil
}

// cachedRefresh 执行一次 revalidate: TTL 窗口内直接返回 nil (沿用当前快照,
// 缓存条目一定比当前快照旧或相同, 不会把权限回滚到更宽的状态); 窗口外真实
// 调用, 成功的刷新写入缓存。错误不缓存, 吊销即时传递。
func (g *Gate) cachedRefresh(ctx context.Context, grant *Grant, check RecheckFunc, cache *refreshCache) (*Grant, error) {
	if check == nil {
		return nil, nil
	}
	g.mu.Lock()
	ttl, last := g.revalidateTTL, *cache
	g.mu.Unlock()
	if ttl > 0 && last.grant != nil && g.now()-last.at < ttl.Milliseconds() {
		return nil, nil
	}
	refreshed, err := check(ctx, grant)
	if err != nil {
		return nil, err
	}
	if refreshed != nil && ttl > 0 {
		g.mu.Lock()
		*cache = refreshCache{grant: refreshed, at: g.now()}
		g.mu.Unlock()
	}
	return refreshed, nil
}

// PipeOutput 把终端输出拷贝给 viewer, 直到源结束、上下文结束或授权失效。
func (g *Gate) PipeOutput(ctx context.Context, dst io.Writer, src io.Reader) error {
	return g.pipe(ctx, dst, src, false)
}

// PipeInput 把 viewer 输入拷贝给终端; 只读授权拒绝且不放行任何字节。
func (g *Gate) PipeInput(ctx context.Context, dst io.Writer, src io.Reader) error {
	return g.pipe(ctx, dst, src, true)
}

func (g *Gate) refreshInput(ctx context.Context) error {
	g.refreshMu.Lock()
	defer g.refreshMu.Unlock()
	refreshed, err := g.cachedRefresh(ctx, g.Grant(), g.inputCheck, &g.inputCache)
	if err != nil {
		return err
	}
	if refreshed != nil {
		g.setGrant(refreshed)
	}
	return nil
}

func (g *Gate) pipe(ctx context.Context, dst io.Writer, src io.Reader, write bool) error {
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := g.Check(ctx); err != nil {
			return err
		}
		if write && !g.InputAllowed() {
			return ErrInputNotAllowed
		}
		count, readErr := src.Read(buffer)
		if err := ctx.Err(); err != nil {
			return err
		}
		if count > 0 {
			if write && g.inputCheck != nil {
				if err := g.refreshInput(ctx); err != nil {
					return err
				}
			} else {
				if err := g.Check(ctx); err != nil {
					return err
				}
			}
			if write && !g.InputAllowed() {
				return ErrInputNotAllowed
			}
			if _, err := dst.Write(buffer[:count]); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
