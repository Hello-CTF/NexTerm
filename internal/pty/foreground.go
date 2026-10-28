package pty

import "errors"

// ErrForegroundUnsupported reports a platform or terminal that cannot report
// its foreground process group or line discipline, such as a Windows
// pseudo console.
var ErrForegroundUnsupported = errors.New("terminal foreground state is not available on this platform")

// Foreground describes who reads the terminal right now.
type Foreground struct {
	// LeaderPID is the process the session was started with. It leads the
	// terminal session and its own process group.
	LeaderPID int
	// ProcessGroup is the terminal's foreground process group.
	ProcessGroup int
	// Canonical reports ICANON on the terminal: input is read a line at a
	// time. CanonicalKnown is false when the mode could not be read.
	Canonical      bool
	CanonicalKnown bool
}

// LeaderInForeground reports whether the started process's own group holds
// the terminal, i.e. no job it launched is in the foreground.
func (f Foreground) LeaderInForeground() bool {
	return f.ProcessGroup == f.LeaderPID
}

type foregroundReporter interface {
	foreground() (Foreground, error)
}

// Foreground reads the terminal's foreground process group and line
// discipline. It returns ErrForegroundUnsupported where the platform has no
// such notion (Windows ConPTY).
func (s *Session) Foreground() (Foreground, error) {
	reporter, ok := s.terminal.(foregroundReporter)
	if !ok {
		return Foreground{}, ErrForegroundUnsupported
	}
	return reporter.foreground()
}
