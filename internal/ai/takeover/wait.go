package takeover

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
)

func (m *Manager) waitFor(ctx context.Context, tabID, pattern string, timeoutMS int64) (tools.Output, error) {
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return tools.Fail(fmt.Errorf("正则无效: %w", err)), nil
	}
	if timeoutMS == 0 {
		timeoutMS = 10_000
	}
	if timeoutMS < 1 || timeoutMS > 60_000 {
		return tools.Fail(fmt.Errorf("timeout_ms 必须在 1-60000 之间")), nil
	}
	waitContext, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		screen, err := m.deps.Snapshot(waitContext, tabID)
		if err != nil {
			if ctx.Err() != nil {
				return tools.Output{}, ctx.Err()
			}
			return tools.Fail(err), nil
		}
		tail := screen.Tail
		if len(tail) == 0 {
			tail = strings.Split(screen.Text, "\n")
		}
		if len(tail) > 30 {
			tail = tail[len(tail)-30:]
		}
		if regex.MatchString(strings.Join(tail, "\n")) {
			return tools.OK("模式已出现"), nil
		}
		select {
		case <-ctx.Done():
			return tools.Output{}, ctx.Err()
		case <-waitContext.Done():
			if len(tail) > 10 {
				tail = tail[len(tail)-10:]
			}
			return tools.Output{Text: "等待超时，最近输出：\n" + strings.Join(tail, "\n"), ExitCode: 1}, nil
		case <-ticker.C:
		}
	}
}
