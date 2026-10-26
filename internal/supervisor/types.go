package supervisor

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

const (
	maxDimension          = 1024
	defaultCommandTimeout = 5 * time.Second
)

var ErrMetadataConflict = errors.New("supervisor session metadata changed")

type Config struct {
	StateDir       string
	CommandTimeout time.Duration
}

type CreateOptions struct {
	ID      string
	Attempt string
	Command []string
	Dir     string
	Env     []string
	Cols    uint32
	Rows    uint32
	// Pty is nil or true for a PTY session; false requests a one-shot non-PTY
	// exec session. Non-PTY sessions take no input, are excluded from List,
	// cannot be attached, persist nothing, and are dropped when the process
	// exits.
	Pty *bool
}

func (o CreateOptions) usePTY() bool {
	return o.Pty == nil || *o.Pty
}

type Info struct {
	ID string `json:"id"`
	// Generation is the helper generation that reported this info; it
	// changes when the helper restarts.
	Generation       string     `json:"generation,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	Incarnation      string     `json:"incarnation"`
	Title            string     `json:"title,omitempty"`
	MetadataRevision uint64     `json:"metadata_revision,omitempty"`
	Cols             uint32     `json:"cols"`
	Rows             uint32     `json:"rows"`
	Dead             bool       `json:"dead"`
	ExitCode         *int       `json:"exit_code,omitempty"`
	Signal           string     `json:"signal,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

type RenameRequest struct {
	ID                       string `json:"id"`
	Incarnation              string `json:"incarnation"`
	RequestID                string `json:"request_id"`
	ExpectedMetadataRevision uint64 `json:"expected_metadata_revision"`
	Title                    string `json:"title"`
}

type MetadataConflictError struct {
	Current Info
	message string
}

func (e *MetadataConflictError) Error() string {
	if e.message != "" {
		return e.message
	}
	return fmt.Sprintf("%s: %s", ErrMetadataConflict, e.Current.ID)
}

func (e *MetadataConflictError) Unwrap() error {
	return ErrMetadataConflict
}

type Identity struct {
	CreatedAt   time.Time
	Incarnation string
	// Generation is the helper generation the identity was observed in. A
	// non-empty value makes attach fail with ErrGenerationMismatch once the
	// helper has restarted; it is not part of the session's own identity.
	Generation string
}

// sameIdentity compares session identities; the helper generation is checked
// separately.
func sameIdentity(left, right Identity) bool {
	return left.Incarnation == right.Incarnation && left.CreatedAt.Equal(right.CreatedAt)
}

func normalizeCreate(options CreateOptions) (CreateOptions, string, error) {
	id := options.ID
	if id == "" {
		id = ids.New()
	}
	if !ids.Valid(id) {
		return CreateOptions{}, "", fmt.Errorf("%w: invalid supervisor session ID", ErrInvalidInput)
	}
	if options.Attempt != "" && !ids.Valid(options.Attempt) {
		return CreateOptions{}, "", fmt.Errorf("%w: invalid supervisor create attempt", ErrInvalidInput)
	}
	if options.Cols == 0 {
		options.Cols = 80
	}
	if options.Rows == 0 {
		options.Rows = 24
	}
	if err := validateSize(options.Cols, options.Rows); err != nil {
		return CreateOptions{}, "", err
	}
	command, err := launchCommand(options)
	if err != nil {
		return CreateOptions{}, "", err
	}
	options.Command = command
	return options, id, nil
}

func launchCommand(options CreateOptions) ([]string, error) {
	command := options.Command
	if len(command) == 0 {
		command = defaultShellCommand(options.Env)
	}
	if command[0] == "" {
		return nil, fmt.Errorf("%w: empty shell executable", ErrInvalidInput)
	}
	for _, argument := range command {
		if strings.ContainsRune(argument, 0) {
			return nil, fmt.Errorf("%w: NUL in shell command", ErrInvalidInput)
		}
	}
	for _, entry := range options.Env {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" || strings.ContainsRune(entry, 0) {
			return nil, fmt.Errorf("%w: environment entries must be KEY=VALUE", ErrInvalidInput)
		}
	}
	if strings.ContainsRune(options.Dir, 0) {
		return nil, fmt.Errorf("%w: NUL in working directory", ErrInvalidInput)
	}
	return command, nil
}

func validateSize(cols, rows uint32) error {
	if cols == 0 || rows == 0 || cols > maxDimension || rows > maxDimension {
		return fmt.Errorf("%w: dimensions must be between 1 and %d", ErrInvalidInput, maxDimension)
	}
	return nil
}

func environmentValue(environment []string, name string) string {
	value := ""
	for _, entry := range environment {
		if key, current, found := strings.Cut(entry, "="); found && key == name {
			value = current
		}
	}
	return value
}

func mergedEnv(overrides []string) []string {
	if len(overrides) == 0 {
		return nil
	}
	environment := os.Environ()
	for _, entry := range overrides {
		key, _, _ := strings.Cut(entry, "=")
		replaced := false
		for index, existing := range environment {
			existingKey, _, found := strings.Cut(existing, "=")
			if found && envKeyEqual(existingKey, key) {
				environment[index] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			environment = append(environment, entry)
		}
	}
	return environment
}
