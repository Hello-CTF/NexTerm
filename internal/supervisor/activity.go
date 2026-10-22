package supervisor

import (
	"context"
	"fmt"
)

// ActivitySnapshot counts live or starting processes across the whole daemon,
// independent of which client created or attached them. Exec counts non-PTY
// sessions that the durable session List intentionally hides.
type ActivitySnapshot struct {
	PTY  int `json:"pty"`
	Exec int `json:"exec"`
}

func (s ActivitySnapshot) Total() int {
	return s.PTY + s.Exec
}

func (s ActivitySnapshot) Active() bool {
	return s.Total() != 0
}

func (s ActivitySnapshot) RequireIdle() error {
	if !s.Active() {
		return nil
	}
	return fmt.Errorf("%w: %d PTY and %d non-PTY exec", ErrActiveSessions, s.PTY, s.Exec)
}

func (s *Supervisor) startActivityLocked(usePTY bool) {
	if usePTY {
		s.startingPTY++
	} else {
		s.startingExec++
	}
}

func (s *Supervisor) finishActivityStartLocked(usePTY bool) {
	if usePTY {
		s.startingPTY--
	} else {
		s.startingExec--
	}
}

// Activity takes a point-in-time snapshot without changing any session or
// attachment. Dead and crash-recovered sessions remain durable history only.
func (s *Supervisor) Activity(ctx context.Context) (ActivitySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ActivitySnapshot{}, context.Cause(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := ActivitySnapshot{PTY: s.startingPTY, Exec: s.startingExec}
	for _, session := range s.sessions {
		session.mu.RLock()
		dead := session.dead
		session.mu.RUnlock()
		if dead {
			continue
		}
		if session.exec != nil {
			snapshot.Exec++
		} else {
			snapshot.PTY++
		}
	}
	return snapshot, nil
}

// Activity returns a fresh daemon-wide snapshot from each negotiated hello.
// known is false when an older daemon cannot prove that it is idle.
func (c *Client) Activity(ctx context.Context) (snapshot ActivitySnapshot, known bool, err error) {
	conn, err := c.dialConn(ctx)
	if err != nil {
		return ActivitySnapshot{}, false, err
	}
	defer func() { _ = conn.Close() }()
	if conn.activity == nil {
		return ActivitySnapshot{}, false, nil
	}
	return *conn.activity, true, nil
}

// RequireIdle fails closed when activity exists or the daemon cannot provide a
// daemon-wide snapshot. It never kills or migrates a process.
func (c *Client) RequireIdle(ctx context.Context) error {
	snapshot, known, err := c.Activity(ctx)
	if err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("%w: daemon-wide activity is unavailable", ErrUnsupported)
	}
	return snapshot.RequireIdle()
}
