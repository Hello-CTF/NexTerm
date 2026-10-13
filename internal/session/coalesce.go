package session

import (
	"context"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

const (
	outputCoalesceInterval = 8 * time.Millisecond
	outputCoalesceMax      = 64 << 10
)

// outCoalescer 是 subscriber 侧的输出合并缓冲: 首块达到上限直接发, 否则短窗
// 追加, 窗口到点或缓冲到上限整批发, 超限的批次不截断。所有发送路径 (feed
// 同步发, 定时器 flush) 都在 feedGate 下进行, 保证与 attach 重放/快照帧的
// 相对顺序; teardown 的兜底 flush 用 TrySend 不阻塞, 有在途发送时放弃兜底,
// 避免与在途批次乱序。
type outCoalescer struct {
	mu      sync.Mutex
	buf     []byte
	timer   *time.Timer
	sending bool
	gone    bool
	onTimer func()
}

func newOutCoalescer(onTimer func()) *outCoalescer {
	return &outCoalescer{onTimer: onTimer}
}

// add 追加 data; 返回需要立即发送的批次 (首块超限直发或缓冲到上限整批),
// 未达上限时缓冲并起窗口计时, 返回 sendNow=false。
func (c *outCoalescer) add(data []byte) (batch []byte, sendNow bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return nil, false
	}
	if len(c.buf) == 0 && len(data) >= outputCoalesceMax {
		c.sending = true
		return data, true
	}
	c.buf = append(c.buf, data...)
	if len(c.buf) >= outputCoalesceMax {
		batch := c.buf
		c.buf = nil
		if c.timer != nil {
			c.timer.Stop()
			c.timer = nil
		}
		c.sending = true
		return batch, true
	}
	if c.timer == nil {
		c.timer = time.AfterFunc(outputCoalesceInterval, c.onTimer)
	}
	return nil, false
}

// take 取走窗口到点时的缓冲批次供定时器 flush 发送。
func (c *outCoalescer) take() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer = nil
	if c.gone || len(c.buf) == 0 {
		return nil
	}
	batch := c.buf
	c.buf = nil
	c.sending = true
	return batch
}

func (c *outCoalescer) sent() {
	c.mu.Lock()
	c.sending = false
	c.mu.Unlock()
}

// teardown 停窗并标记移除; 无在途发送时把残余缓冲 TrySend 兜底 (尽力而为,
// 队满或通道已替换即丢弃, 与 producer.Close 丢弃队列的既有语义一致)。
func (c *outCoalescer) teardown(producer *hub.Producer) {
	c.mu.Lock()
	c.gone = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	batch := c.buf
	c.buf = nil
	flush := !c.sending && len(batch) > 0
	c.mu.Unlock()
	if flush {
		_ = producer.TrySendBinary(context.Background(), batch)
	}
}
