package session

import (
	"context"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/mattn/go-runewidth"
)

// Command sources recorded in the command log.
const (
	// CommandSourceTerminal marks a command observed through OSC 133 shell
	// integration in an interactive terminal tab. The command text is the
	// completed input line read off the tab screen at the 133;B boundary.
	CommandSourceTerminal = "terminal"
	// CommandSourceExec marks a command executed through the WinRM line-mode
	// exec path, where the exact command line and exit code are known.
	CommandSourceExec = "exec"
)

// CommandRecord is one audited command executed in a terminal tab.
type CommandRecord struct {
	SessionID  string
	TabID      string
	AssetID    string
	UserID     string
	Command    string
	Source     string
	ExitCode   *int
	StartedAt  time.Time
	FinishedAt time.Time
}

// CommandSink receives audited commands. The session feed path calls it
// synchronously once per finished command, so implementations should return
// quickly.
type CommandSink interface {
	RecordCommand(ctx context.Context, record CommandRecord)
}

type CommandSinkFunc func(context.Context, CommandRecord)

func (f CommandSinkFunc) RecordCommand(ctx context.Context, record CommandRecord) {
	f(ctx, record)
}

// pendingCommand is a started command awaiting its finish report. It is only
// touched with the tab feed gate held.
type pendingCommand struct {
	sequence  uint64
	command   string
	startedAt time.Time
}

// maxCommandWrapRows bounds how many screen rows a single command line may
// span when long input wrapped across rows.
const maxCommandWrapRows = 8

// observeCommand scans tab output for OSC 133 command lifecycle reports,
// records finished commands through the manager's sink, and stores the
// latest lifecycle snapshot. Callers must hold the tab feed gate: the
// tracker is not safe for concurrent use. The recording, replay, and
// subscriber bytes are untouched.
func (m *Manager) observeCommand(tab *Tab, data []byte) {
	reports, state, _ := tab.commandTrack.ObserveReports(data)
	if len(reports) > 0 {
		now := time.Now()
		for _, report := range reports {
			switch {
			case report.Letter == 'B' && report.Started:
				// The previous command never reported a finish; close it with an
				// unknown exit code instead of dropping it.
				m.finishCommand(tab, now, nil)
				m.startCommand(tab, report.Sequence, now)
			case report.Letter == 'D' || report.Letter == 'A':
				var exitCode *int
				if pending := tab.pendingCmd; pending != nil && report.Sequence == pending.sequence && report.HasExitCode {
					code := report.ExitCode
					exitCode = &code
				}
				m.finishCommand(tab, now, exitCode)
			}
		}
	}
	tab.mu.Lock()
	tab.commandState = state
	tab.mu.Unlock()
}

func (m *Manager) startCommand(tab *Tab, sequence uint64, now time.Time) {
	if m.commands == nil {
		return
	}
	command := tab.commandLine()
	if command == "" {
		return
	}
	tab.pendingCmd = &pendingCommand{sequence: sequence, command: command, startedAt: now}
}

func (m *Manager) finishCommand(tab *Tab, now time.Time, exitCode *int) {
	pending := tab.pendingCmd
	if pending == nil {
		return
	}
	tab.pendingCmd = nil
	if m.commands == nil {
		return
	}
	m.commands.RecordCommand(context.Background(), CommandRecord{
		SessionID:  tab.SessionID,
		TabID:      tab.ID,
		AssetID:    tab.session.asset.ID,
		UserID:     tab.session.userID,
		Command:    pending.command,
		Source:     CommandSourceTerminal,
		ExitCode:   exitCode,
		StartedAt:  pending.startedAt,
		FinishedAt: now,
	})
}

// flushPendingCommand closes a still-running command when its tab closes, so
// long-lived commands are not lost from the audit trail.
func (m *Manager) flushPendingCommand(tab *Tab) {
	m.finishCommand(tab, time.Now(), nil)
}

// recordExecCommand audits a WinRM line-mode command, where the exact command
// line and exit code are known without screen extraction.
func (m *Manager) recordExecCommand(ctx context.Context, tab *Tab, command string, result base.ExecResult) {
	if m.commands == nil || command == "" {
		return
	}
	finished := time.Now()
	m.commands.RecordCommand(ctx, CommandRecord{
		SessionID:  tab.SessionID,
		TabID:      tab.ID,
		AssetID:    tab.session.asset.ID,
		UserID:     tab.session.userID,
		Command:    command,
		Source:     CommandSourceExec,
		ExitCode:   result.ExitCode,
		StartedAt:  finished.Add(-result.Duration),
		FinishedAt: finished,
	})
}

// CommandState returns the latest OSC 133 command lifecycle snapshot for the
// tab: whether a command is running, how many have started, and the last
// reported exit code.
func (t *Tab) CommandState() shellintegr.CommandState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commandState
}

// commandLine reads the completed command line off the tab screen. The shell
// prints OSC 133;B right after the accepted input line was echoed, so the
// command sits on the rows at or just above the cursor. It returns "" when
// the terminal does not expose a screen snapshot.
func (t *Tab) commandLine() string {
	core, ok := t.terminal.(interface{ Snapshot() terminal.Snapshot })
	if !ok {
		return ""
	}
	return commandLineFromSnapshot(core.Snapshot())
}

func commandLineFromSnapshot(snapshot terminal.Snapshot) string {
	lines := snapshot.Lines
	if len(lines) == 0 {
		return ""
	}
	row := snapshot.CursorRow
	if row >= len(lines) {
		row = len(lines) - 1
	}
	// A fresh empty line at column 0 means the newline was already echoed:
	// the command is the row above. Otherwise the cursor is still on the
	// command row itself.
	if snapshot.CursorCol == 0 && strings.TrimSpace(lines[row]) == "" && row > 0 {
		row--
	}
	start := row
	// The command row is the last row of a possibly wrapped input line: while
	// the row above is completely filled the input spilled into the current
	// top row, so keep collecting upward, newest row last.
	for start > 0 && runewidth.StringWidth(lines[start-1]) >= snapshot.Cols && row-start+1 < maxCommandWrapRows {
		start--
	}
	parts := make([]string, 0, row-start+1)
	for i := start; i <= row; i++ {
		parts = append(parts, lines[i])
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}
