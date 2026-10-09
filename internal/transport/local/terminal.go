package local

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/pty"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func (t *Transport) OpenPTY(ctx context.Context, options base.PTYOptions) (base.Channel, error) {
	return t.openPTY(ctx, nil, options)
}

func (t *Transport) OpenCommandPTY(ctx context.Context, command string, options base.PTYOptions) (base.Channel, error) {
	args := shellArguments(command)
	return t.openPTY(ctx, args, options)
}

func (t *Transport) openPTY(ctx context.Context, args []string, options base.PTYOptions) (base.Channel, error) {
	if err := t.check(ctx, options.ExpectedGeneration); err != nil {
		return nil, err
	}
	session, err := pty.Start(ctx, pty.Config{
		Path:       t.shell,
		Args:       args,
		Dir:        t.cwd,
		Env:        environment(t.env, true, options.Term),
		Cols:       options.Cols,
		Rows:       options.Rows,
		ID:         t.channelID("pty"),
		Generation: t.generation,
	})
	if err != nil {
		return nil, err
	}
	return t.track(ctx, session)
}
