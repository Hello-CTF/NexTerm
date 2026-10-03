package durable

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const fieldSeparator = "|"

var discoveryFormat = strings.Join([]string{
	"#{session_name}",
	"#{session_id}",
	"#{window_id}",
	"#{session_created}",
	"#{@nexterm_durable_id}",
	"#{@nexterm_durable_namespace}",
	"#{@nexterm_durable_pane}",
	"#{@nexterm_durable_window}",
	"#{pane_id}",
	"#{pane_pid}",
	"#{pane_dead}",
	"#{pane_dead_status}",
	"#{pane_pipe}",
	"#{pane_dead_signal}",
	"#{window_width}",
	"#{window_height}",
}, fieldSeparator)

type record struct {
	info          Info
	name          string
	windowID      string
	owner         string
	namespace     string
	paneOption    string
	windowOption  string
	deadStatus    string
	recordingLive bool
	cols          uint32
	rows          uint32
}

func (b *Backend) List(ctx context.Context) ([]Info, error) {
	records, err := b.records(ctx)
	if err != nil {
		return nil, err
	}
	primary := make(map[string]record)
	seen := make(map[string]bool)
	for _, current := range records {
		id, owned, matches := b.ownedRecord(current)
		if !owned {
			continue
		}
		seen[id] = true
		if !matches {
			continue
		}
		if _, duplicate := primary[id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate primary pane for %s", ErrIdentity, id)
		}
		primary[id] = current
	}
	for id := range seen {
		if _, ok := primary[id]; !ok {
			return nil, fmt.Errorf("%w: primary pane for %s is missing", ErrIdentity, id)
		}
	}
	result := make([]Info, 0, len(primary))
	for _, current := range primary {
		result = append(result, cloneInfo(current.info))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (b *Backend) resolve(ctx context.Context, id string) (record, error) {
	if !ids.Valid(id) {
		return record{}, fmt.Errorf("%w: invalid durable terminal ID", ErrInvalidInput)
	}
	records, err := b.records(ctx)
	if err != nil {
		return record{}, err
	}
	name := b.sessionName(id)
	seen := false
	foreign := false
	var found *record
	for index := range records {
		current := records[index]
		if current.name == name && (current.namespace != b.namespace || current.owner != id) {
			foreign = true
		}
		ownedID, owned, matches := b.ownedRecord(current)
		if !owned || ownedID != id {
			continue
		}
		seen = true
		if !matches {
			continue
		}
		if found != nil {
			return record{}, fmt.Errorf("%w: duplicate primary pane for %s", ErrIdentity, id)
		}
		copy := current
		found = &copy
	}
	if found != nil {
		return *found, nil
	}
	if seen {
		return record{}, fmt.Errorf("%w: primary pane for %s is missing", ErrIdentity, id)
	}
	if foreign {
		return record{}, fmt.Errorf("%w: %s", ErrNotOwned, name)
	}
	return record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
}

func (b *Backend) ownedRecord(current record) (id string, owned, matches bool) {
	if current.namespace != b.namespace || current.owner == "" {
		return "", false, false
	}
	if !ids.Valid(current.owner) || current.name != b.sessionName(current.owner) {
		return "", false, false
	}
	matches = current.paneOption == current.info.PaneID &&
		current.windowOption == current.windowID &&
		validTmuxID(current.info.SessionID, '$') &&
		validTmuxID(current.info.PaneID, '%') &&
		validTmuxID(current.windowID, '@')
	return current.owner, true, matches
}

func (b *Backend) records(ctx context.Context) ([]record, error) {
	if _, err := os.Stat(b.socketPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	// -a enumerates panes from every session on the server. Without it tmux
	// lists only its internally selected current session, so a second durable
	// session on the same socket becomes invisible to discovery and is
	// mistaken for an exited one.
	stdout, err := b.run(ctx, "list-panes", "-a", "-F", discoveryFormat)
	if err != nil {
		if noServerError(err) {
			return nil, nil
		}
		if _, statErr := os.Stat(b.socketPath); errors.Is(statErr, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return parseRecords(stdout)
}

func noServerError(err error) bool {
	var command *commandError
	if !errors.As(err, &command) {
		return false
	}
	if strings.HasPrefix(command.stderr, "no server running on ") || command.stderr == "no sessions" {
		return true
	}
	return strings.HasPrefix(command.stderr, "error connecting to ") &&
		(strings.Contains(command.stderr, "(No such file or directory)") || strings.Contains(command.stderr, "(Connection refused)"))
}

func parseRecords(output []byte) ([]record, error) {
	text := strings.TrimRight(string(output), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	result := make([]record, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, fieldSeparator)
		if len(fields) != 16 {
			return nil, fmt.Errorf("malformed tmux discovery record: got %d fields in %q", len(fields), line)
		}
		created, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || created <= 0 {
			return nil, fmt.Errorf("malformed tmux session creation time %q", fields[3])
		}
		pid, err := strconv.Atoi(fields[9])
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("malformed tmux pane PID %q", fields[9])
		}
		dead, err := parseFlag(fields[10])
		if err != nil {
			return nil, fmt.Errorf("malformed tmux pane dead flag: %w", err)
		}
		pipe, err := parseFlag(fields[12])
		if err != nil {
			return nil, fmt.Errorf("malformed tmux pane pipe flag: %w", err)
		}
		cols, err := parseDimension(fields[14])
		if err != nil {
			return nil, fmt.Errorf("malformed tmux window width: %w", err)
		}
		rows, err := parseDimension(fields[15])
		if err != nil {
			return nil, fmt.Errorf("malformed tmux window height: %w", err)
		}
		info := Info{
			ID:        fields[4],
			SessionID: fields[1],
			PaneID:    fields[8],
			PID:       pid,
			CreatedAt: time.Unix(created, 0),
			Dead:      dead,
			Cols:      cols,
			Rows:      rows,
		}
		if dead && fields[11] != "" {
			status, err := strconv.Atoi(fields[11])
			if err != nil {
				return nil, fmt.Errorf("malformed tmux pane exit status %q", fields[11])
			}
			info.ExitCode = &status
		}
		if dead {
			info.Signal = fields[13]
		}
		result = append(result, record{
			info:          info,
			name:          fields[0],
			windowID:      fields[2],
			owner:         fields[4],
			namespace:     fields[5],
			paneOption:    fields[6],
			windowOption:  fields[7],
			deadStatus:    fields[11],
			recordingLive: pipe,
			cols:          cols,
			rows:          rows,
		})
	}
	return result, nil
}

func parseFlag(value string) (bool, error) {
	switch value {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("expected 0 or 1, got %q", value)
	}
}

// parseDimension accepts any positive window dimension; consumers apply
// their own size limits before using it (an externally resized tmux window
// beyond the supported maximum must not make discovery fail closed).
func parseDimension(value string) (uint32, error) {
	dimension, err := strconv.ParseUint(value, 10, 32)
	if err != nil || dimension == 0 {
		return 0, fmt.Errorf("expected a positive dimension, got %q", value)
	}
	return uint32(dimension), nil
}

func validTmuxID(value string, prefix byte) bool {
	if len(value) < 2 || value[0] != prefix {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func cloneInfo(info Info) Info {
	if info.ExitCode != nil {
		status := *info.ExitCode
		info.ExitCode = &status
	}
	return info
}

func sameIdentity(left, right Info) bool {
	return left.ID == right.ID &&
		left.SessionID == right.SessionID &&
		left.PaneID == right.PaneID &&
		left.PID == right.PID &&
		left.CreatedAt.Equal(right.CreatedAt)
}
