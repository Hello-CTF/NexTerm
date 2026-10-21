package production

import (
	"context"
	"io"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fakeSSHConnector struct{}

func (fakeSSHConnector) Connect(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
	return &fakeSSHTransport{kind: asset.Kind, generation: generation}, nil
}

type fakeSSHTransport struct {
	kind       string
	generation uint64
	closed     bool
}

func (t *fakeSSHTransport) Kind() string       { return t.kind }
func (t *fakeSSHTransport) Generation() uint64 { return t.generation }
func (t *fakeSSHTransport) IsAlive() bool      { return !t.closed }
func (t *fakeSSHTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *fakeSSHTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *fakeSSHTransport) Close() error {
	t.closed = true
	return nil
}
func (t *fakeSSHTransport) OpenPTY(_ context.Context, _ base.PTYOptions) (base.Channel, error) {
	return &fakePTYChannel{id: "fake-pty", generation: t.generation}, nil
}

type fakePTYChannel struct {
	id         string
	generation uint64
}

func (c *fakePTYChannel) Read([]byte) (int, error)                     { return 0, io.EOF }
func (c *fakePTYChannel) Write(data []byte) (int, error)               { return len(data), nil }
func (c *fakePTYChannel) Close() error                                 { return nil }
func (c *fakePTYChannel) Stderr() io.Reader                            { return nil }
func (c *fakePTYChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *fakePTYChannel) Wait(context.Context) error                   { return nil }
func (c *fakePTYChannel) CloseWrite() error                            { return nil }
func (c *fakePTYChannel) ID() string                                   { return c.id }
func (c *fakePTYChannel) Generation() uint64                           { return c.generation }
