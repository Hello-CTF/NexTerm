package sharing

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
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
// read_write 授权放行; 每个数据块在 Read 前与 Write 前各复查一次, 授权失效
// 后双向立即停止, 失效或取消期间读到的块一律丢弃。阻塞的 Read 由集成层在
// 取消时关闭 source 唤醒。
type Gate struct {
	mu         sync.Mutex
	grant      *Grant
	now        func() int64
	recheck    RecheckFunc
	inputCheck func(ctx context.Context, grant *Grant) (*Grant, error)
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
	grant := g.Grant()
	if grant == nil {
		return errors.New("sharing: nil grant")
	}
	g.mu.Lock()
	recheck := g.recheck
	g.mu.Unlock()
	if recheck != nil {
		refreshed, err := recheck(ctx, grant)
		if err != nil {
			return err
		}
		if refreshed != nil {
			g.setGrant(refreshed)
		}
	}
	if g.now() >= g.Grant().ExpiresAt {
		return ErrGrantExpired
	}
	return nil
}

// PipeOutput 把终端输出拷贝给 viewer, 直到源结束、上下文结束或授权失效。
func (g *Gate) PipeOutput(ctx context.Context, dst io.Writer, src io.Reader) error {
	return g.pipe(ctx, dst, src, false)
}

// PipeInput 把 viewer 输入拷贝给终端; 只读授权拒绝且不放行任何字节。
func (g *Gate) PipeInput(ctx context.Context, dst io.Writer, src io.Reader) error {
	return g.pipe(ctx, dst, src, true)
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
				refreshed, err := g.inputCheck(ctx, g.Grant())
				if err != nil {
					return err
				}
				if refreshed != nil {
					g.setGrant(refreshed)
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
