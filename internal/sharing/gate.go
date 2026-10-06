package sharing

import (
	"context"
	"errors"
	"io"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

var (
	// ErrInputNotAllowed 表示只读授权的输入方向被 agent 拒绝。
	ErrInputNotAllowed = errors.New("sharing: grant is read-only")
	// ErrGrantExpired 表示授权已过有效期, 双向都必须停止。
	ErrGrantExpired = errors.New("sharing: grant expired")
)

// RecheckFunc 让 agent 在会话中途重新校验授权 (例如向服务端确认分享未被
// 吊销); 返回错误即双向停止。
type RecheckFunc func(ctx context.Context, grant *Grant) error

// Gate 是 agent 端的分享执行点: 输出方向 (终端到 viewer) 始终放行, 输入
// 方向仅对 read_write 授权放行; 每个数据块拷贝前复查有效期与吊销状态,
// 授权失效后双向立即停止。
type Gate struct {
	grant   *Grant
	now     func() int64
	recheck RecheckFunc
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

func NewGate(grant *Grant, options ...GateOption) *Gate {
	g := &Gate{grant: grant, now: ids.NowMS}
	for _, option := range options {
		option(g)
	}
	return g
}

func (g *Gate) Grant() *Grant { return g.grant }

func (g *Gate) InputAllowed() bool {
	return g.grant != nil && g.grant.Permission.AllowsWrite()
}

// Check 报告授权是否仍然可用: 过期或 recheck 失败 (吊销) 都返回错误。
func (g *Gate) Check(ctx context.Context) error {
	if g.grant == nil {
		return errors.New("sharing: nil grant")
	}
	if g.now() >= g.grant.ExpiresAt {
		return ErrGrantExpired
	}
	if g.recheck != nil {
		if err := g.recheck(ctx, g.grant); err != nil {
			return err
		}
	}
	return nil
}

// PipeOutput 把终端输出拷贝给 viewer, 直到源结束、上下文结束或授权失效。
// 阻塞的 Read 由集成层在取消时关闭 source 唤醒; 取消/吊销期间读到的块
// 在 Write 前的复查中被丢弃。
func (g *Gate) PipeOutput(ctx context.Context, dst io.Writer, src io.Reader) error {
	return g.pipe(ctx, dst, src)
}

// PipeInput 把 viewer 输入拷贝给终端; 只读授权立即拒绝且不放行任何字节。
func (g *Gate) PipeInput(ctx context.Context, dst io.Writer, src io.Reader) error {
	if !g.InputAllowed() {
		return ErrInputNotAllowed
	}
	return g.pipe(ctx, dst, src)
}

func (g *Gate) pipe(ctx context.Context, dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := g.Check(ctx); err != nil {
			return err
		}
		count, readErr := src.Read(buffer)
		if count > 0 {
			if err := g.Check(ctx); err != nil {
				return err
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
