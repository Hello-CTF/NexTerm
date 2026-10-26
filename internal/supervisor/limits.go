package supervisor

import (
	"fmt"
	"strconv"
	"time"
)

// Limits bound how many processes one helper runs and how fast clients may
// start them, so a runaway script that loops on create is refused with
// ErrLimit instead of exhausting process IDs or memory. The defaults are far
// above interactive use.
type Limits struct {
	// MaxSessions is the most live or starting sessions, PTY and exec together.
	MaxSessions int
	// MaxCreates is the most creates accepted within CreateWindow.
	MaxCreates   int
	CreateWindow time.Duration
}

var DefaultLimits = Limits{MaxSessions: 512, MaxCreates: 120, CreateWindow: 10 * time.Second}

func (l Limits) withDefaults() Limits {
	if l.MaxSessions == 0 {
		l.MaxSessions = DefaultLimits.MaxSessions
	}
	if l.MaxCreates == 0 {
		l.MaxCreates = DefaultLimits.MaxCreates
	}
	if l.CreateWindow == 0 {
		l.CreateWindow = DefaultLimits.CreateWindow
	}
	return l
}

func (l Limits) validate() error {
	if l.MaxSessions < 0 || l.MaxCreates < 0 || l.CreateWindow < 0 {
		return fmt.Errorf("%w: negative supervisor limit", ErrInvalidInput)
	}
	return nil
}

// admitCreateLocked charges one create against the limits. It must run under
// s.mu before the create counts itself as starting.
func (s *Supervisor) admitCreateLocked() error {
	live := s.startingPTY + s.startingExec
	for _, session := range s.sessions {
		session.mu.RLock()
		dead := session.dead
		session.mu.RUnlock()
		if !dead {
			live++
		}
	}
	if live >= s.limits.MaxSessions {
		return fmt.Errorf("%w: %d sessions are running and the helper allows %d", ErrLimit, live, s.limits.MaxSessions)
	}
	now := s.now()
	cutoff := now.Add(-s.limits.CreateWindow)
	kept := s.recentCreates[:0]
	for _, at := range s.recentCreates {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	s.recentCreates = kept
	if len(s.recentCreates) >= s.limits.MaxCreates {
		return fmt.Errorf("%w: %d sessions were created in the last %s and the helper allows %d", ErrLimit, len(s.recentCreates), s.limits.CreateWindow, s.limits.MaxCreates)
	}
	s.recentCreates = append(s.recentCreates, now)
	return nil
}

const (
	maxSessionsOption  = "--max-sessions"
	maxCreatesOption   = "--max-creates"
	createWindowOption = "--create-window"
)

func limitOption(name string) bool {
	return name == maxSessionsOption || name == maxCreatesOption || name == createWindowOption
}

func (l *Limits) set(name, value string) error {
	if name == createWindowOption {
		window, err := time.ParseDuration(value)
		if err != nil || window <= 0 {
			return fmt.Errorf("option %s requires a positive duration", name)
		}
		l.CreateWindow = window
		return nil
	}
	count, err := strconv.Atoi(value)
	if err != nil || count <= 0 {
		return fmt.Errorf("option %s requires a positive integer", name)
	}
	if name == maxSessionsOption {
		l.MaxSessions = count
	} else {
		l.MaxCreates = count
	}
	return nil
}

// args renders the configured limits as helper command-line options.
func (l Limits) args() []string {
	var args []string
	if l.MaxSessions > 0 {
		args = append(args, maxSessionsOption, strconv.Itoa(l.MaxSessions))
	}
	if l.MaxCreates > 0 {
		args = append(args, maxCreatesOption, strconv.Itoa(l.MaxCreates))
	}
	if l.CreateWindow > 0 {
		args = append(args, createWindowOption, l.CreateWindow.String())
	}
	return args
}
