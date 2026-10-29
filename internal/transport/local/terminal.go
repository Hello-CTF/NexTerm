package local

import (
	"context"
	"path/filepath"
	"runtime"

	"github.com/Hello-CTF/NexTerm/internal/pty"
	"github.com/Hello-CTF/NexTerm/internal/shellenv"
	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func (t *Transport) OpenPTY(ctx context.Context, options base.PTYOptions) (base.Channel, error) {
	return t.openPTY(ctx, nil, options)
}

func (t *Transport) OpenCommandPTY(ctx context.Context, command string, options base.PTYOptions) (base.Channel, error) {
	args := shellArguments(t.loginShell, command)
	return t.openPTY(ctx, args, options)
}

func (t *Transport) openPTY(ctx context.Context, args []string, options base.PTYOptions) (base.Channel, error) {
	if err := t.check(ctx, options.ExpectedGeneration); err != nil {
		return nil, err
	}
	env := environmentFor(t.shell, t.env, true, options.Term)
	env = append(env, shellintegr.BashBannerEnv(runtime.GOOS, t.shell, env)...)
	session, err := pty.Start(ctx, pty.Config{
		Path:       t.shell,
		Args:       args,
		Dir:        t.cwd,
		Env:        env,
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

// DurableLaunch is what a local terminal run by the local helper starts with:
// the same shell and directory as OpenPTY. SHELL and PATH are passed explicitly: the long-lived helper
// may have been started with a stale or minimal environment, so new terminals must not depend on it.
// The helper resolves the rest of the login environment itself.
func (t *Transport) DurableLaunch() (command []string, dir string, env []string) {
	env = []string{"TERM=xterm-256color"}
	if runtime.GOOS != "windows" {
		resolved := shellenv.Default().Env(resolveWait)
		if filepath.IsAbs(t.shell) {
			env = append(env, "SHELL="+t.shell)
		}
		if path := shellenv.EnvValue(resolved, "PATH"); path != "" {
			env = append(env, "PATH="+path)
		}
	}
	for key, value := range t.env {
		env = append(env, key+"="+value)
	}
	return []string{t.shell}, t.cwd, env
}
