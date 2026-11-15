package ssh

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// writeStopGrace is how long a write whose context ended may take to stop on its own. A write between requests, or
// waiting on an awaited lookup, notices the context and returns its own result within it; one still waiting after it
// is blocked on an unanswered mutating request.
var writeStopGrace = time.Second

// maxAbandonedWrites bounds the abandoned writes one connection keeps waiting in the background, each holding a
// goroutine and its content until the server answers or the connection closes.
const maxAbandonedWrites = 8

// writePhase records whether a write has sent its first mutating request. begin refuses once ctx has ended, so a
// lookup that answers after the caller stopped waiting never starts a write the caller was told did not happen.
type writePhase struct {
	ctx      context.Context
	mu       sync.Mutex
	mutating bool
}

func (p *writePhase) begin() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ctx.Err(); err != nil {
		return err
	}
	p.mutating = true
	return nil
}

func (p *writePhase) started() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mutating
}

// boundWrite runs write and stops waiting for it when ctx ends. pkg/sftp requests take no context, so a server blocked
// on a hung mount would otherwise hold the caller forever on a mutating request that await deliberately does not
// abandon.
//
// write calls begin before its first mutating request. Until then the write is still a lookup: lookups stop waiting
// through await, so a write that ends with ctx before begin returns ctx's error and nothing was changed. Once a
// mutating request is in flight the outcome is unknown: the request keeps running and the server may still apply it,
// so the caller gets a *base.WriteOutcomeUnknownError instead of a cancellation. The abandoned write keeps ctx, so
// every later step of it sees the context ended and stops before committing; its deferred cleanup removes what it
// staged, and the path stays refused for new writes until it returns (see admitWrite).
func (f *FS) boundWrite(ctx context.Context, remotePath string, write func(begin func() error) error) error {
	if err := f.admitWrite(remotePath); err != nil {
		return err
	}
	phase := &writePhase{ctx: ctx}
	if ctx.Done() == nil {
		return write(phase.begin)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- write(phase.begin) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	grace := time.NewTimer(writeStopGrace)
	defer grace.Stop()
	select {
	case err := <-done:
		return err
	case <-grace.C:
	}
	f.abandonWrite(remotePath, done)
	if !phase.started() {
		return ctx.Err()
	}
	return &base.WriteOutcomeUnknownError{Path: remotePath, Reason: ctx.Err()}
}

// admitWrite refuses a write to a path whose abandoned write is still running, so its late commit cannot land over the
// newer content, and any write once maxAbandonedWrites are still running on this connection.
func (f *FS) admitWrite(remotePath string) error {
	f.abandonedMu.Lock()
	defer f.abandonedMu.Unlock()
	if f.abandoned[remotePath] > 0 {
		return fmt.Errorf("SFTP write %s: an earlier write to this file stopped waiting for the server and may still apply; nothing was written, read the file again before retrying", remotePath)
	}
	if f.abandonedTotal >= maxAbandonedWrites {
		return fmt.Errorf("SFTP write %s: %d earlier writes on this connection are still waiting for the server; nothing was written, reconnect or retry later", remotePath, f.abandonedTotal)
	}
	return nil
}

// abandonWrite keeps remotePath refused until the abandoned write returns.
func (f *FS) abandonWrite(remotePath string, done <-chan error) {
	f.abandonedMu.Lock()
	if f.abandoned == nil {
		f.abandoned = make(map[string]int)
	}
	f.abandoned[remotePath]++
	f.abandonedTotal++
	f.abandonedMu.Unlock()
	go func() {
		<-done
		f.abandonedMu.Lock()
		if f.abandoned[remotePath]--; f.abandoned[remotePath] == 0 {
			delete(f.abandoned, remotePath)
		}
		f.abandonedTotal--
		f.abandonedMu.Unlock()
	}()
}
