package aicontext

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

var reconCommands = []string{
	"hostname; uname -a; whoami; uptime",
	"df -h | head -15",
	"free -m 2>/dev/null | head -5",
	"ss -lntp 2>/dev/null | head -25 || netstat -lntp 2>/dev/null | head -25",
	"systemctl --failed --no-pager 2>/dev/null | head -15",
	"echo \"== docker ps（总数 $(docker ps -q 2>/dev/null | wc -l)）==\"; docker ps --format '{{.Names}} {{.Status}}' 2>/dev/null | head -60",
}

func (b *Builder) cachedRecon(ctx context.Context, sessionID string) string {
	now := b.deps.Now()
	b.mu.Lock()
	entry := b.recon[sessionID]
	if entry != nil && now.Before(entry.expires) {
		text := entry.text
		b.mu.Unlock()
		return text
	}
	if entry != nil && entry.ready != nil {
		ready := entry.ready
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ""
		case <-ready:
		}
		b.mu.Lock()
		text := entry.text
		b.mu.Unlock()
		return text
	}
	entry = &reconEntry{ready: make(chan struct{})}
	b.recon[sessionID] = entry
	b.mu.Unlock()

	text := ""
	transport, err := b.deps.Transport(ctx, sessionID)
	if err == nil {
		text = Recon(ctx, transport)
	}

	b.mu.Lock()
	entry.text = text
	entry.expires = b.deps.Now().Add(b.deps.ReconTTL)
	close(entry.ready)
	entry.ready = nil
	b.mu.Unlock()
	return text
}

func Recon(ctx context.Context, transport base.Transport) string {
	var output strings.Builder
	for _, command := range reconCommands {
		if ctx.Err() != nil {
			break
		}
		commandContext, cancel := context.WithTimeout(ctx, 8*time.Second)
		result, err := transport.Exec(commandContext, command, base.ExecOptions{Timeout: 8 * time.Second, ExpectedGeneration: transport.Generation()})
		cancel()
		fmt.Fprintf(&output, "$ %s\n", command)
		if err != nil {
			fmt.Fprintf(&output, "<侦察命令失败: %v>\n", err)
			continue
		}
		stdout := result.Stdout
		truncated := result.Truncated
		lines := strings.Split(stdout, "\n")
		if len(lines) > 400 {
			stdout = strings.Join(lines[:400], "\n")
			truncated = true
		}
		if len(stdout) > 120<<10 {
			stdout = prefixBytes(stdout, 120<<10)
			truncated = true
		}
		output.WriteString(strings.TrimRight(stdout, "\n"))
		output.WriteByte('\n')
		if truncated {
			output.WriteString("（上一条命令输出超出预算已截断）\n")
		}
	}
	return output.String()
}
