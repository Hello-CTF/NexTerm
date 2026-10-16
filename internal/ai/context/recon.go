package aicontext

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var reconCommands = []string{
	"hostname; uname -a; whoami; uptime",
	"df -h | head -15",
	"free -m 2>/dev/null | head -5",
	"ss -lntp 2>/dev/null | head -25 || netstat -lntp 2>/dev/null | head -25",
	"systemctl --failed --no-pager 2>/dev/null | head -15",
	"echo \"== docker ps (total $(docker ps -q 2>/dev/null | wc -l)) ==\"; docker ps --format '{{.Names}} {{.Status}}' 2>/dev/null | head -60",
}

func (b *Builder) cachedRecon(ctx context.Context, sessionID string) string {
	transport, err := b.deps.Transport(ctx, sessionID)
	if err != nil {
		return ""
	}
	key := reconCacheKey{sessionID: sessionID, ownerScope: ipc.OwnerID(ctx)}
	now := b.deps.Now()
	b.mu.Lock()
	entry := b.recon[key]
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
	b.recon[key] = entry
	b.mu.Unlock()

	text, succeeded := reconResult(ctx, transport)

	b.mu.Lock()
	if succeeded && text != "" {
		entry.text = text
		entry.expires = b.deps.Now().Add(b.deps.ReconTTL)
	} else {
		delete(b.recon, key)
	}
	close(entry.ready)
	entry.ready = nil
	b.mu.Unlock()
	return text
}

func Recon(ctx context.Context, transport base.Transport) string {
	text, _ := reconResult(ctx, transport)
	return text
}

func reconResult(ctx context.Context, transport base.Transport) (string, bool) {
	succeeded := true
	var output strings.Builder
	for _, command := range reconCommands {
		if ctx.Err() != nil {
			succeeded = false
			break
		}
		commandContext, cancel := context.WithTimeout(ctx, 8*time.Second)
		result, err := transport.Exec(commandContext, command, base.ExecOptions{Timeout: 8 * time.Second, ExpectedGeneration: transport.Generation()})
		cancel()
		fmt.Fprintf(&output, "$ %s\n", command)
		if err != nil {
			succeeded = false
			fmt.Fprintf(&output, "<recon command failed: %v>\n", err)
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
			output.WriteString("(previous command output exceeded the budget and was truncated)\n")
		}
	}
	return output.String(), succeeded && ctx.Err() == nil
}
