package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeSkillLoader struct {
	schema  Schema
	output  Output
	gotArgs json.RawMessage
}

func (f *fakeSkillLoader) LoadSkillSchema() Schema { return f.schema }

func (f *fakeSkillLoader) LoadSkill(_ context.Context, args json.RawMessage) Output {
	f.gotArgs = append(json.RawMessage(nil), args...)
	return f.output
}

func loadSkillTestSchema() Schema {
	return Schema{
		Name:        "load_skill",
		Description: "Load a skill by name.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required": []any{"name"},
		},
	}
}

func TestRegistryExposesLoadSkillWhenLoaderConfigured(t *testing.T) {
	loader := &fakeSkillLoader{schema: loadSkillTestSchema(), output: OK("pinned material")}
	registry := NewRegistry(Dependencies{Skills: loader})

	exposed := false
	for _, schema := range registry.schemas {
		if schema.Name == "load_skill" {
			exposed = true
		}
	}
	if !exposed {
		t.Fatal("load_skill schema is missing from the registry")
	}
	if err := registry.Validate(Call{Name: "load_skill", Args: json.RawMessage(`{"name":"alpha-skill"}`)}); err != nil {
		t.Fatalf("valid load_skill call rejected: %v", err)
	}
	if err := registry.Validate(Call{Name: "load_skill", Args: json.RawMessage(`{"name":"alpha-skill","extra":1}`)}); err == nil {
		t.Fatal("unknown argument accepted")
	}
	if err := registry.Validate(Call{Name: "load_skill", Args: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("missing name accepted")
	}

	output := registry.Execute(context.Background(), "job", Scope{}, Call{ID: "call-1", Name: "load_skill", Args: json.RawMessage(`{"name":"alpha-skill"}`)}, nil)
	if !output.OK || output.Text != "pinned material" {
		t.Fatalf("output = %+v", output)
	}
	if string(loader.gotArgs) != `{"name":"alpha-skill"}` {
		t.Fatalf("loader received args %s", loader.gotArgs)
	}
}

func TestRegistryWithoutLoaderKeepsLoadSkillUnavailable(t *testing.T) {
	registry := NewRegistry(Dependencies{})
	if err := registry.Validate(Call{Name: "load_skill", Args: json.RawMessage(`{"name":"alpha-skill"}`)}); err == nil {
		t.Fatal("load_skill accepted without a configured loader")
	}
	output := registry.Execute(context.Background(), "job", Scope{}, Call{ID: "call-1", Name: "load_skill", Args: json.RawMessage(`{"name":"alpha-skill"}`)}, nil)
	if output.OK {
		t.Fatalf("output = %+v", output)
	}
}

func TestPlanAllowedIncludesLoadSkill(t *testing.T) {
	if !PlanAllowed("load_skill") {
		t.Fatal("load_skill is read-only and must be plan-allowed")
	}
}
