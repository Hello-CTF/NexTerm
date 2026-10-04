package supervisor

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const (
	maxDimension          = 1024
	defaultCommandTimeout = 5 * time.Second
)

type Config struct {
	StateDir       string
	CommandTimeout time.Duration
}

type CreateOptions struct {
	ID      string
	Command []string
	Dir     string
	Env     []string
	Cols    uint32
	Rows    uint32
}

type Info struct {
	ID          string     `json:"id"`
	CreatedAt   time.Time  `json:"created_at"`
	Incarnation string     `json:"incarnation"`
	Cols        uint32     `json:"cols"`
	Rows        uint32     `json:"rows"`
	Dead        bool       `json:"dead"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	Signal      string     `json:"signal,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type Identity struct {
	CreatedAt   time.Time
	Incarnation string
}

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
