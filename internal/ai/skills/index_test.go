package skills_test

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
)

func syntheticFS() fstest.MapFS {
	return fstest.MapFS{
		"embedded/alpha-skill.md":       &fstest.MapFile{Data: []byte("---\nname: alpha-skill\ndescription: Alpha synthetic skill\n---\nalpha body\n")},
		"embedded/nested/beta-skill.md": &fstest.MapFile{Data: []byte("---\nname: beta-skill\ndescription: Beta synthetic skill\n---\nbeta body\n")},
		"embedded/notes.txt":            &fstest.MapFile{Data: []byte("not a skill")},
	}
}

func TestBuildIndex(t *testing.T) {
	index, err := skills.BuildIndex(syntheticFS())
	if err != nil {
		t.Fatal(err)
	}
	names := index.Names()
	if len(names) != 2 || names[0] != "alpha-skill" || names[1] != "beta-skill" {
		t.Fatalf("names = %v", names)
	}
	skill, ok := index.Lookup("beta-skill")
	if !ok || skill.Body != "beta body\n" || skill.Description != "Beta synthetic skill" {
		t.Fatalf("beta = %+v, %v", skill, ok)
	}
	if _, ok := index.Lookup("missing"); ok {
		t.Fatal("lookup of missing skill succeeded")
	}
	all := index.Skills()
	if len(all) != 2 || all[0].Name != "alpha-skill" || all[1].Name != "beta-skill" {
		t.Fatalf("skills = %+v", all)
	}
}

func TestBuildIndexRejectsNameMismatch(t *testing.T) {
	fsys := fstest.MapFS{"embedded/wrong-name.md": &fstest.MapFile{Data: []byte("---\nname: other-name\ndescription: demo\n---\nbody\n")}}
	if _, err := skills.BuildIndex(fsys); !errors.Is(err, skills.ErrInvalidSkill) {
		t.Fatalf("err = %v, want ErrInvalidSkill", err)
	}
}

func TestBuildIndexRejectsDuplicateNames(t *testing.T) {
	fsys := fstest.MapFS{
		"a/demo.md": &fstest.MapFile{Data: []byte("---\nname: demo\ndescription: first\n---\nbody\n")},
		"b/demo.md": &fstest.MapFile{Data: []byte("---\nname: demo\ndescription: second\n---\nbody\n")},
	}
	if _, err := skills.BuildIndex(fsys); !errors.Is(err, skills.ErrInvalidSkill) {
		t.Fatalf("err = %v, want ErrInvalidSkill", err)
	}
}

func TestBuildIndexRejectsInvalidDocument(t *testing.T) {
	fsys := fstest.MapFS{"embedded/broken.md": &fstest.MapFile{Data: []byte("no front matter")}}
	if _, err := skills.BuildIndex(fsys); !errors.Is(err, skills.ErrInvalidSkill) {
		t.Fatalf("err = %v, want ErrInvalidSkill", err)
	}
}

func TestNoBuiltInSkillsShip(t *testing.T) {
	manager, err := skills.NewManager(t.Context(), newMemorySettings())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := manager.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none until the user writes a skill", entries)
	}
	catalog, err := manager.SkillCatalog(t.Context())
	if err != nil || len(catalog) != 0 {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}
}

func TestSkillCatalogListsOnlyEnabledSkills(t *testing.T) {
	manager, err := skills.NewManager(t.Context(), newMemorySettings())
	if err != nil {
		t.Fatal(err)
	}
	description, body := "检查 nginx 配置", "nginx -t\n"
	for _, input := range []skills.OverrideInput{
		{Name: "nginx-check", Description: &description, Body: &body},
		{Name: "old-check", Description: &description, Body: &body, Disabled: true},
	} {
		if _, err := manager.SaveOverride(t.Context(), input); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := manager.SkillCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || catalog[0].Name != "nginx-check" || catalog[0].Description != description {
		t.Fatalf("catalog = %+v, want only the enabled skill", catalog)
	}
}
