package session

import (
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// LocalDurableSource supplies the durable provider of this machine's local
// helper. WaitReady returns the provider, waiting up to limit while the
// helper is still starting; otherwise it returns an error that wraps
// durable.ErrUnavailable and whose message is the user-visible reason.
type LocalDurableSource interface {
	WaitReady(ctx context.Context, limit time.Duration) (base.DurableProvider, error)
	// WaitExisting returns a provider for terminals that already exist,
	// which a helper that cannot take new terminals may still serve.
	WaitExisting(ctx context.Context, limit time.Duration) (base.DurableProvider, error)
}

const (
	// localOpenWait bounds how long opening a new local terminal waits for a
	// helper that is still starting before it opens a regular terminal.
	localOpenWait = time.Second
	// localRecoverWait bounds the wait when an existing durable terminal is
	// listed, recovered or killed, which only a ready helper can do.
	localRecoverWait = 5 * time.Second
)

// localLaunchSource is implemented by the local transport: the shell,
// directory and environment additions a local terminal starts with, so a
// durable terminal started by the helper matches a regular one.
type localLaunchSource interface {
	DurableLaunch() (command []string, dir string, env []string)
}

// localDurableEnabled reports whether this manager runs local terminals
// through a helper at all.
func (m *Manager) localDurableEnabled() bool {
	return m.localDurable != nil || m.durable != nil
}

func (m *Manager) localDurableProvider(ctx context.Context, wait time.Duration, existing bool) (base.DurableProvider, error) {
	if m.localDurable != nil {
		if existing {
			return m.localDurable.WaitExisting(ctx, wait)
		}
		return m.localDurable.WaitReady(ctx, wait)
	}
	if m.durable == nil {
		return nil, ErrUnsupported
	}
	return m.durable, nil
}

// localDurableReadiness reports, without waiting, whether a new terminal on a
// connected local session would be durable: the local helper is ready, still
// starting, or unavailable for a reason the tab shows.
func (m *Manager) localDurableReadiness(ctx context.Context, connected *Session) (DurableReadiness, error) {
	if mode, err := m.terminalModeFor(ctx, connected); err != nil {
		return DurableReadiness{}, err
	} else if mode == TerminalModeRegular {
		return DurableReadiness{State: DurableDisabled}, nil
	}
	if !m.localDurableEnabled() {
		return DurableReadiness{State: DurableUnavailable, Reason: "此环境没有本机持久组件"}, nil
	}
	_, err := m.localDurableProvider(ctx, 0, false)
	if err == nil {
		return DurableReadiness{State: DurableReady}, nil
	}
	if ctx.Err() != nil {
		return DurableReadiness{}, context.Cause(ctx)
	}
	var starting interface{ Starting() bool }
	if errors.As(err, &starting) && starting.Starting() {
		return DurableReadiness{State: DurablePreparing}, nil
	}
	return DurableReadiness{State: DurableUnavailable, Reason: localUnavailableReason(err)}, nil
}

func localUnavailableReason(err error) string {
	if errors.Is(err, ErrUnsupported) {
		return "此环境没有本机持久组件"
	}
	return err.Error()
}

// localFallbackNotice explains a local terminal that opened regular because
// the helper was not ready.
func localFallbackNotice(cause error) string {
	return "本机持久组件不可用（" + localUnavailableReason(cause) + "），已打开普通本机终端；关闭标签或退出 NexTerm 会结束它"
}

// claimLocalFallbackNotice reports whether this reason has not been shown for
// the session yet, so repeated tabs do not repeat the same notice while a
// changed reason is shown again.
func (s *Session) claimLocalFallbackNotice(notice string) bool {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	if s.localFallbackNotice == notice {
		return false
	}
	s.localFallbackNotice = notice
	return true
}

func (s *Session) clearLocalFallbackNotice() {
	s.durableMu.Lock()
	s.localFallbackNotice = ""
	s.durableMu.Unlock()
}

// localHelperUnavailable reports a local durable failure that a regular
// terminal may replace: the helper is not ready, or it went away between
// readiness and create. A limit refusal is not one of them.
func localHelperUnavailable(err error) bool {
	return errors.Is(err, durable.ErrUnavailable) && !errors.Is(err, durable.ErrLimit) || errors.Is(err, ErrUnsupported)
}

// durableFallbackAllowed reports whether a durable open that failed with err
// may become a regular terminal: only a new terminal that asked for the
// fallback, on a live transport, whose helper is unavailable.
func durableFallbackAllowed(ctx context.Context, kind string, options OpenTabOptions, transport *transportHandle, err error) bool {
	if options.Durable == nil || !options.DurableFallback || options.Durable.Recover || ctx.Err() != nil || !transport.Transport.IsAlive() {
		return false
	}
	switch kind {
	case KindSSH:
		return sshHelperUnavailable(err)
	case KindLocal:
		return localHelperUnavailable(err)
	}
	return false
}

// regularFallbackOptions turns a durable open into a regular one. A regular
// local terminal keeps the in-process semantics: it lives in this process and
// may be moved to the background until the app exits.
func regularFallbackOptions(kind string, options OpenTabOptions) OpenTabOptions {
	options.Durable = nil
	if kind == KindLocal {
		options.Ephemeral = true
		options.Retainable = !options.Detached
	}
	return options
}

// localFallbackNoticeFor returns the notice of a local terminal that opened
// regular. A detached open (an AI-created terminal) always carries it; an
// interactive one carries it once per reason.
func (m *Manager) localFallbackNoticeFor(session *Session, detached bool, cause error) string {
	notice := localFallbackNotice(cause)
	if detached || session.claimLocalFallbackNotice(notice) {
		m.logger.Warn("local durable terminal unavailable; using a regular PTY", "session", session.ID, "error", cause)
		return notice
	}
	return ""
}
