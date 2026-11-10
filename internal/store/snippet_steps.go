package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	SnippetMaxSteps        = 64
	SnippetMaxStepIDBytes  = 96
	SnippetMaxNameBytes    = 512
	SnippetMaxDescBytes    = 4096
	SnippetMaxCommandBytes = 64 << 10
	SnippetMaxWaitForBytes = 1024
	// A step timeout of zero uses the client's default wait.
	SnippetMinWaitTimeoutMs = 1000
	SnippetMaxWaitTimeoutMs = 10 * 60 * 1000
)

// SnippetStep is one command of a multi-step snippet. Confirm asks for an
// explicit confirmation before the step runs. WaitFor holds the step back,
// while the user runs the snippet in a terminal, until that terminal's output
// matches it (plain text, or a regular expression when WaitForRegex is set);
// the run stops after TimeoutMs.
type SnippetStep struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Command      string `json:"command"`
	Confirm      bool   `json:"confirm,omitempty"`
	Interactive  bool   `json:"interactive,omitempty"`
	WaitFor      string `json:"waitFor,omitempty"`
	WaitForRegex bool   `json:"waitForRegex,omitempty"`
	TimeoutMs    int64  `json:"timeoutMs,omitempty"`
}

// SnippetContent is the validated content a snippet write carries beyond its
// name and placement. Zero Steps means a plain single-step snippet whose
// command is Body.
type SnippetContent struct {
	Description string
	Body        string
	Steps       []SnippetStep
}

// ValidateSnippetSteps applies the shared bounds to a step list received
// through IPC, sync or import.
func ValidateSnippetSteps(steps []SnippetStep) error {
	if len(steps) > SnippetMaxSteps {
		return badParam(fmt.Errorf("snippet steps must contain at most %d entries", SnippetMaxSteps))
	}
	seen := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		if step.ID == "" || len(step.ID) > SnippetMaxStepIDBytes || strings.TrimSpace(step.ID) != step.ID {
			return badParam(fmt.Errorf("invalid snippet step id"))
		}
		if _, dup := seen[step.ID]; dup {
			return badParam(fmt.Errorf("duplicate snippet step id %s", step.ID))
		}
		seen[step.ID] = struct{}{}
		if len(step.Name) > SnippetMaxNameBytes {
			return badParam(fmt.Errorf("snippet step name exceeds %d bytes", SnippetMaxNameBytes))
		}
		if len(step.Description) > SnippetMaxDescBytes {
			return badParam(fmt.Errorf("snippet step description exceeds %d bytes", SnippetMaxDescBytes))
		}
		if strings.TrimSpace(step.Command) == "" || len(step.Command) > SnippetMaxCommandBytes {
			return badParam(fmt.Errorf("snippet step command must be non-empty and at most %d bytes", SnippetMaxCommandBytes))
		}
		if err := validateSnippetStepWait(step); err != nil {
			return err
		}
	}
	return nil
}

// validateSnippetStepWait bounds the wait condition. A regular expression must
// compile as RE2: clients match it with an RE2 engine (linear time), never as a
// JavaScript RegExp, so the stored syntax means the same everywhere.
func validateSnippetStepWait(step SnippetStep) error {
	if step.WaitFor == "" {
		if step.WaitForRegex || step.TimeoutMs != 0 {
			return badParam(fmt.Errorf("snippet step wait options require waitFor"))
		}
		return nil
	}
	if strings.TrimSpace(step.WaitFor) == "" || len(step.WaitFor) > SnippetMaxWaitForBytes {
		return badParam(fmt.Errorf("snippet step waitFor must contain non-space text and at most %d bytes", SnippetMaxWaitForBytes))
	}
	if step.TimeoutMs != 0 && (step.TimeoutMs < SnippetMinWaitTimeoutMs || step.TimeoutMs > SnippetMaxWaitTimeoutMs) {
		return badParam(fmt.Errorf("snippet step timeoutMs must be between %d and %d", SnippetMinWaitTimeoutMs, SnippetMaxWaitTimeoutMs))
	}
	if step.WaitForRegex {
		if _, err := regexp.Compile(step.WaitFor); err != nil {
			return badParam(fmt.Errorf("snippet step waitFor is not a valid regular expression: %w", err))
		}
	}
	return nil
}

// ValidateSnippetDescription bounds the snippet-level description.
func ValidateSnippetDescription(description string) error {
	if len(description) > SnippetMaxDescBytes {
		return badParam(fmt.Errorf("snippet description exceeds %d bytes", SnippetMaxDescBytes))
	}
	return nil
}

// SnippetStepsBody derives the body of a multi-step snippet: the step
// commands joined by newlines, used for search and by clients without
// step support.
func SnippetStepsBody(steps []SnippetStep) string {
	commands := make([]string, len(steps))
	for i, step := range steps {
		commands[i] = step.Command
	}
	return strings.Join(commands, "\n")
}

// NormalizeSnippetContent validates the content and returns the stored
// description, body and encoded steps. A multi-step body is always derived
// from the steps and the supplied body is ignored.
func NormalizeSnippetContent(content SnippetContent) (description, body, encodedSteps string, err error) {
	if err := ValidateSnippetDescription(content.Description); err != nil {
		return "", "", "", err
	}
	if err := ValidateSnippetSteps(content.Steps); err != nil {
		return "", "", "", err
	}
	if len(content.Steps) == 0 {
		return content.Description, content.Body, "", nil
	}
	encoded, err := json.Marshal(content.Steps)
	if err != nil {
		return "", "", "", dbError(err)
	}
	return content.Description, SnippetStepsBody(content.Steps), string(encoded), nil
}

// DecodeSnippetSteps parses the stored steps column; an empty column is a
// single-step snippet and yields an empty, non-nil slice.
func DecodeSnippetSteps(encoded string) ([]SnippetStep, error) {
	steps := []SnippetStep{}
	if encoded == "" {
		return steps, nil
	}
	if err := json.Unmarshal([]byte(encoded), &steps); err != nil {
		return nil, dbError(fmt.Errorf("decode snippet steps: %w", err))
	}
	return steps, nil
}

// SyncTombstoneKindSnippetSteps tags the deletion marker of a snippet's steps companion object.
const SyncTombstoneKindSnippetSteps = "snippet_steps"

// SnippetStepsObjectID derives the sync object ID of a multi-step snippet's steps
// companion, so every device computes the same ID from the snippet ID alone.
func SnippetStepsObjectID(snippetID string) string {
	return deriveObjectID("nexterm:snippet-steps:" + snippetID)
}
