package skills

import (
	"fmt"
	"strings"
)

// Parse parses one skill document. The document must open with a "---"
// line, carry name and description front-matter fields, close with a "---"
// line, and have a non-empty markdown body. CRLF input is normalized to LF;
// leading blank lines of the body are trimmed and everything else is kept
// verbatim so digests pin the exact dispatched bytes.
func Parse(data []byte) (Skill, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return Skill{}, fmt.Errorf("%w: missing front matter delimiter", ErrInvalidSkill)
	}
	var header []string
	closing := -1
	for index := 1; index < len(lines); index++ {
		if lines[index] == "---" {
			closing = index
			break
		}
		header = append(header, lines[index])
	}
	if closing < 0 {
		return Skill{}, fmt.Errorf("%w: unterminated front matter", ErrInvalidSkill)
	}
	if len(strings.Join(header, "\n")) > maxFrontMatterBytes {
		return Skill{}, fmt.Errorf("%w: front matter too large", ErrInvalidSkill)
	}
	fields := map[string]string{}
	for _, line := range header {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			return Skill{}, fmt.Errorf("%w: invalid front matter line %q", ErrInvalidSkill, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "name", "description":
		default:
			return Skill{}, fmt.Errorf("%w: unknown front matter key %q", ErrInvalidSkill, key)
		}
		if _, duplicate := fields[key]; duplicate {
			return Skill{}, fmt.Errorf("%w: duplicate front matter key %q", ErrInvalidSkill, key)
		}
		if value == "" {
			return Skill{}, fmt.Errorf("%w: empty front matter value for %q", ErrInvalidSkill, key)
		}
		fields[key] = value
	}
	skill := Skill{
		Name:        fields["name"],
		Description: fields["description"],
		Body:        strings.TrimLeft(strings.Join(lines[closing+1:], "\n"), "\n"),
	}
	if skill.Name == "" {
		return Skill{}, fmt.Errorf("%w: missing front matter name", ErrInvalidSkill)
	}
	if skill.Description == "" {
		return Skill{}, fmt.Errorf("%w: missing front matter description", ErrInvalidSkill)
	}
	if err := validateDocument(skill); err != nil {
		return Skill{}, err
	}
	return skill, nil
}
