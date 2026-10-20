package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxNameBytes        = 64
	maxDescriptionBytes = 256
	maxBodyBytes        = 64 * 1024
	maxFrontMatterBytes = 4 * 1024
)

var (
	ErrSkillNotFound = errors.New("AI 技能不存在，可能已被删除或覆盖已移除")
	ErrSkillDisabled = errors.New("AI 技能已被禁用，请在设置中启用后重试")
	ErrInvalidSkill  = errors.New("AI 技能内容无效")
)

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Source identifies where the effective skill content originates.
type Source string

const (
	SourceEmbedded Source = "embedded"
	SourceCustom   Source = "custom"
)

// Skill is one parsed skill document: front-matter metadata plus the
// markdown body that is dispatched to the model as instructions.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

// Digest pins the exact material a dispatch would send. Fields are framed
// as "<byte-length>:<value>" so values containing separators cannot make
// distinct dispatched tuples collide.
func (s Skill) Digest() string {
	sum := sha256.Sum256([]byte(canonicalMaterial(s.Name, s.Description, s.Body)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DigestOf is the package-level form of Skill.Digest.
func DigestOf(skill Skill) string {
	return skill.Digest()
}

func canonicalMaterial(fields ...string) string {
	var builder strings.Builder
	for _, field := range fields {
		builder.WriteString(strconv.Itoa(len(field)))
		builder.WriteString(":")
		builder.WriteString(field)
	}
	return builder.String()
}

// Override is the per-owner persisted delta applied on top of an embedded
// skill, or a full custom skill when no embedded skill carries the name.
//
// Override carries model-instruction content only. It must never grow
// guard-rule fields (guard patterns, screen.json): guard configuration is
// owned by the guard package, and user skill overrides affect the model
// instructions dispatched to the provider and nothing else.
type Override struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Body        *string `json:"body,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
	UpdatedAt   int64   `json:"updatedAt"`
}

// OverrideInput is the user-supplied payload for SaveOverride.
type OverrideInput struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Body        *string `json:"body,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
}

// Entry is the effective per-owner view of one skill for Settings.
type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      Source `json:"source"`
	Overridden  bool   `json:"overridden"`
	Disabled    bool   `json:"disabled"`
	Digest      string `json:"digest"`
	UpdatedAt   int64  `json:"updatedAt"`
	// Override carries the owner's persisted delta when present so the
	// Settings editor can prefill exactly which fields are overridden.
	Override *Override `json:"override,omitempty"`
}

// Dispatch is the pinned material produced for the load_skill path: the
// exact instructions plus the digest of what was dispatched.
type Dispatch struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Digest      string `json:"digest"`
	Source      Source `json:"source"`
}

func validateName(name string) error {
	if name == "" || len(name) > maxNameBytes || !namePattern.MatchString(name) {
		return fmt.Errorf("%w: invalid skill name %q", ErrInvalidSkill, name)
	}
	return nil
}

func validateDocument(skill Skill) error {
	if err := validateName(skill.Name); err != nil {
		return err
	}
	if strings.TrimSpace(skill.Description) == "" || len(skill.Description) > maxDescriptionBytes {
		return fmt.Errorf("%w: invalid description for %q", ErrInvalidSkill, skill.Name)
	}
	if strings.TrimSpace(skill.Body) == "" || len(skill.Body) > maxBodyBytes {
		return fmt.Errorf("%w: invalid body for %q", ErrInvalidSkill, skill.Name)
	}
	return nil
}

func validateOverrideInput(name string, hasBase bool, input OverrideInput) error {
	if input.Description != nil {
		trimmed := strings.TrimSpace(*input.Description)
		if trimmed == "" || len(trimmed) > maxDescriptionBytes {
			return fmt.Errorf("%w: invalid override description for %q", ErrInvalidSkill, name)
		}
	}
	if input.Body != nil {
		if strings.TrimSpace(*input.Body) == "" || len(*input.Body) > maxBodyBytes {
			return fmt.Errorf("%w: invalid override body for %q", ErrInvalidSkill, name)
		}
	}
	if !hasBase {
		if input.Description == nil || input.Body == nil {
			return fmt.Errorf("%w: custom skill %q requires description and body", ErrInvalidSkill, name)
		}
		return nil
	}
	if input.Description == nil && input.Body == nil && !input.Disabled {
		return fmt.Errorf("%w: empty override for %q", ErrInvalidSkill, name)
	}
	return nil
}

// effective merges an override over its embedded base. A zero Override with
// hasOverride false yields the base unchanged.
func effective(base Skill, hasBase bool, override Override) Skill {
	if !hasBase {
		skill := Skill{Name: override.Name}
		if override.Description != nil {
			skill.Description = *override.Description
		}
		if override.Body != nil {
			skill.Body = *override.Body
		}
		return skill
	}
	skill := base
	if override.Description != nil {
		skill.Description = *override.Description
	}
	if override.Body != nil {
		skill.Body = *override.Body
	}
	return skill
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
