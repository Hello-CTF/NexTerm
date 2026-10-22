package skills_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func newLoadSkillTestManager(t *testing.T, shared bool) *skills.Manager {
	t.Helper()
	manager, err := skills.NewManager(t.Context(), newMemorySettings(), skills.WithIndex(testIndex(t)), skills.WithShared(shared))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestLoadSkillSchemaDeclaresTheNameParameter(t *testing.T) {
	manager := newLoadSkillTestManager(t, false)
	schema := manager.LoadSkillSchema()
	if schema.Name != skills.LoadSkillToolName {
		t.Fatalf("schema name = %q", schema.Name)
	}
	if schema.Description == "" {
		t.Fatal("schema description is empty")
	}
	properties, _ := schema.Parameters["properties"].(map[string]any)
	if _, ok := properties["name"].(map[string]any); !ok {
		t.Fatalf("schema properties = %+v", schema.Parameters)
	}
	required, _ := schema.Parameters["required"].([]any)
	if len(required) != 1 || required[0] != "name" {
		t.Fatalf("schema required = %+v", schema.Parameters["required"])
	}
}

func TestLoadSkillReturnsPinnedMaterial(t *testing.T) {
	manager := newLoadSkillTestManager(t, false)
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	output := manager.LoadSkill(ctx, json.RawMessage(`{"name":"alpha-skill"}`))
	if !output.OK {
		t.Fatalf("load_skill output = %+v", output)
	}
	dispatch, err := manager.Dispatch(ctx, "alpha-skill")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"skill: alpha-skill", "source: embedded", "digest: " + dispatch.Digest, "Alpha synthetic skill", "alpha body\n"} {
		if !strings.Contains(output.Text, fragment) {
			t.Fatalf("output text missing %q:\n%s", fragment, output.Text)
		}
	}
}

func TestLoadSkillHonorsOverrideAndDisabled(t *testing.T) {
	manager := newLoadSkillTestManager(t, false)
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")

	if _, err := manager.SaveOverride(ctxA, skills.OverrideInput{Name: "alpha-skill", Description: stringPointer("Alpha overridden")}); err != nil {
		t.Fatal(err)
	}
	output := manager.LoadSkill(ctxA, json.RawMessage(`{"name":"alpha-skill"}`))
	if !output.OK || !strings.Contains(output.Text, "Alpha overridden") || !strings.Contains(output.Text, "alpha body\n") {
		t.Fatalf("override output = %+v", output)
	}
	outputB := manager.LoadSkill(ctxB, json.RawMessage(`{"name":"alpha-skill"}`))
	if !outputB.OK || strings.Contains(outputB.Text, "Alpha overridden") {
		t.Fatalf("owner A's override leaked to owner B: %+v", outputB)
	}

	if _, err := manager.SaveOverride(ctxA, skills.OverrideInput{Name: "alpha-skill", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	output = manager.LoadSkill(ctxA, json.RawMessage(`{"name":"alpha-skill"}`))
	if output.OK || !strings.Contains(output.Text, "禁用") {
		t.Fatalf("disabled load_skill output = %+v", output)
	}
	if outputB = manager.LoadSkill(ctxB, json.RawMessage(`{"name":"alpha-skill"}`)); !outputB.OK {
		t.Fatalf("owner B's dispatch must stay available: %+v", outputB)
	}
}

func TestLoadSkillRejectsUnknownAndInvalidNames(t *testing.T) {
	manager := newLoadSkillTestManager(t, false)
	ctx := ipc.WithUserID(t.Context(), "owner-a")

	if output := manager.LoadSkill(ctx, json.RawMessage(`{"name":"missing-skill"}`)); output.OK || !strings.Contains(output.Text, "不存在") {
		t.Fatalf("unknown skill output = %+v", output)
	}
	if output := manager.LoadSkill(ctx, json.RawMessage(`{}`)); output.OK {
		t.Fatalf("empty name output = %+v", output)
	}
	if output := manager.LoadSkill(ctx, json.RawMessage(`{"name":"  "}`)); output.OK {
		t.Fatalf("blank name output = %+v", output)
	}
}

func TestLoadSkillServesLegacySharedState(t *testing.T) {
	manager := newLoadSkillTestManager(t, true)
	output := manager.LoadSkill(t.Context(), json.RawMessage(`{"name":"alpha-skill"}`))
	if !output.OK {
		t.Fatalf("legacy shared load_skill output = %+v", output)
	}
	if _, err := manager.SaveOverride(t.Context(), skills.OverrideInput{Name: "alpha-skill", Body: stringPointer("shared body\n")}); err != nil {
		t.Fatal(err)
	}
	output = manager.LoadSkill(t.Context(), json.RawMessage(`{"name":"alpha-skill"}`))
	if !output.OK || !strings.Contains(output.Text, "shared body\n") {
		t.Fatalf("shared override output = %+v", output)
	}
}
