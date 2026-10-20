package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type blockingSecondReadFS struct {
	*fakeFS
	mu           sync.Mutex
	written      bool
	postWriteCtx context.Context
}

func (b *blockingSecondReadFS) WriteFile(ctx context.Context, name string, content []byte, backup bool) error {
	err := b.fakeFS.WriteFile(ctx, name, content, backup)
	b.mu.Lock()
	b.written = true
	b.mu.Unlock()
	return err
}

func (b *blockingSecondReadFS) ReadFile(ctx context.Context, name string, maximum int64) ([]byte, error) {
	b.mu.Lock()
	written := b.written
	b.mu.Unlock()
	if written {
		b.postWriteCtx = ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.fakeFS.ReadFile(ctx, name, maximum)
}

type blockingReadTransport struct {
	files *blockingSecondReadFS
}

func (b *blockingReadTransport) Kind() string       { return "fake" }
func (b *blockingReadTransport) Generation() uint64 { return 1 }
func (b *blockingReadTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (b *blockingReadTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (b *blockingReadTransport) IsAlive() bool                               { return true }
func (b *blockingReadTransport) Close() error                                { return nil }
func (b *blockingReadTransport) FileSystem(context.Context) (base.FileSystem, error) {
	return b.files, nil
}

func TestR5CompleteWriteSecondReadUsesBoundedContext(t *testing.T) {
	files := &blockingSecondReadFS{fakeFS: newFakeFS()}
	files.files["/a"] = []byte("old")
	transport := &blockingReadTransport{files: files}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	executeTool(registry, "j", "read_file", `{"path":"/a"}`)
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
	elapsed := time.Since(start)
	if !result.OK {
		t.Fatalf("result = %+v", result)
	}
	if elapsed < 4*time.Second || elapsed > 8*time.Second {
		t.Fatalf("post-write read was not bounded by the 5s context: %v", elapsed)
	}
	if files.postWriteCtx == nil {
		t.Fatal("post-write read did not run")
	}
	if _, ok := files.postWriteCtx.Deadline(); !ok {
		t.Fatal("post-write read context has no deadline")
	}
}
