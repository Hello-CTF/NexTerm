package skills

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Index is an immutable name-keyed collection of skills.
type Index struct {
	skills map[string]Skill
	names  []string
}

// BuildIndex parses every ".md" file in fsys. The file base name must match
// the front-matter name, and names must be unique across the tree.
func BuildIndex(fsys fs.FS) (*Index, error) {
	skills := map[string]Skill{}
	err := fs.WalkDir(fsys, ".", func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		data, err := fs.ReadFile(fsys, filePath)
		if err != nil {
			return fmt.Errorf("read skill document %s: %w", filePath, err)
		}
		skill, err := Parse(data)
		if err != nil {
			return fmt.Errorf("parse skill document %s: %w", filePath, err)
		}
		base := strings.TrimSuffix(path.Base(filePath), ".md")
		if base != skill.Name {
			return fmt.Errorf("%w: document %s declares name %q", ErrInvalidSkill, filePath, skill.Name)
		}
		if _, duplicate := skills[skill.Name]; duplicate {
			return fmt.Errorf("%w: duplicate skill name %q", ErrInvalidSkill, skill.Name)
		}
		skills[skill.Name] = skill
		return nil
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)
	return &Index{skills: skills, names: names}, nil
}

// Lookup returns the skill with the given name.
func (i *Index) Lookup(name string) (Skill, bool) {
	if i == nil {
		return Skill{}, false
	}
	skill, ok := i.skills[name]
	return skill, ok
}

// Names returns the sorted skill names.
func (i *Index) Names() []string {
	if i == nil {
		return nil
	}
	return append([]string(nil), i.names...)
}

// Skills returns the skills sorted by name.
func (i *Index) Skills() []Skill {
	if i == nil {
		return nil
	}
	result := make([]Skill, 0, len(i.names))
	for _, name := range i.names {
		result = append(result, i.skills[name])
	}
	return result
}
