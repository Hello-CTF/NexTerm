package memory

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SchemaVersion   = 2
	MaxTopicBytes   = 256
	MaxContentBytes = 64 * 1024
	maxScopeBytes   = 256
)

var (
	ErrNotFound         = errors.New("semantic memory not found")
	ErrPermissionDenied = errors.New("semantic memory access denied")
	ErrVersionConflict  = errors.New("semantic memory version conflict")
	ErrSensitiveContent = errors.New("semantic memory contains a possible secret")
	ErrInvalidInput     = errors.New("invalid semantic memory input")
)

type Scope struct {
	Tenant  string
	Subject string
}

type Entry struct {
	ID        string
	Topic     string
	Content   string
	Version   uint64
	Redacted  bool
	CreatedAt int64
	UpdatedAt int64
}

type SecretPolicy uint8

const (
	SecretReject SecretPolicy = iota
	SecretRedact
)

type CreateInput struct {
	Topic   string
	Content string
	Secrets SecretPolicy
}

type EditInput struct {
	Topic   *string
	Content *string
	Secrets SecretPolicy
}

type IndexEntry struct {
	ID        string
	Version   uint64
	Redacted  bool
	UpdatedAt int64
}

type TopicIndex struct {
	Topic   string
	Entries []IndexEntry
}

type Settings struct {
	InjectionEnabled bool
	ToolsEnabled     bool
	Version          uint64
}

type SettingsInput struct {
	InjectionEnabled *bool
	ToolsEnabled     *bool
}

type VersionConflictError struct {
	ID       string
	Expected uint64
	Actual   uint64
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("%s: %s (expected %d, actual %d)", ErrVersionConflict, e.ID, e.Expected, e.Actual)
}

func (e *VersionConflictError) Unwrap() error {
	return ErrVersionConflict
}

func normalizeScope(scope Scope) (Scope, error) {
	scope.Tenant = strings.TrimSpace(scope.Tenant)
	scope.Subject = strings.TrimSpace(scope.Subject)
	if scope.Tenant == "" || scope.Subject == "" {
		return Scope{}, ErrPermissionDenied
	}
	if !validScopePart(scope.Tenant) || !validScopePart(scope.Subject) {
		return Scope{}, fmt.Errorf("%w: invalid scope", ErrInvalidInput)
	}
	return scope, nil
}

func validScopePart(value string) bool {
	if len(value) > maxScopeBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func normalizeTopic(topic string) (string, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" || len(topic) > MaxTopicBytes || !utf8.ValidString(topic) {
		return "", fmt.Errorf("%w: invalid topic", ErrInvalidInput)
	}
	for _, r := range topic {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: invalid topic", ErrInvalidInput)
		}
	}
	return topic, nil
}

func validateContent(content string) error {
	if strings.TrimSpace(content) == "" || len(content) > MaxContentBytes || !utf8.ValidString(content) {
		return fmt.Errorf("%w: invalid content", ErrInvalidInput)
	}
	return nil
}

func validateID(id string) error {
	if id == "" || len(id) > 64 || !utf8.ValidString(id) {
		return fmt.Errorf("%w: invalid memory ID", ErrInvalidInput)
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: invalid memory ID", ErrInvalidInput)
		}
	}
	return nil
}

func validateSecretPolicy(policy SecretPolicy) error {
	if policy != SecretReject && policy != SecretRedact {
		return fmt.Errorf("%w: unknown secret policy", ErrInvalidInput)
	}
	return nil
}

func sanitize(content string, policy SecretPolicy) (string, bool, error) {
	if err := validateSecretPolicy(policy); err != nil {
		return "", false, err
	}
	redacted, changed := RedactText(content)
	if changed && policy == SecretReject {
		return "", false, ErrSensitiveContent
	}
	if err := validateContent(redacted); err != nil {
		return "", false, err
	}
	return redacted, changed, nil
}
