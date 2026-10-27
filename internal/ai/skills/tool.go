package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

// LoadSkillToolName is the model-facing tool that loads one skill's pinned
// material for injection into the conversation.
const LoadSkillToolName = "load_skill"

// The tools registry consumes the manager through its SkillLoader seam.
var _ tools.SkillLoader = (*Manager)(nil)

// LoadSkillSchema describes the load_skill tool. It lives next to the
// Manager so the tools registry can append it without importing the skills
// package; the registry-side seam consumes this method through a narrow
// interface.
func (m *Manager) LoadSkillSchema() tools.Schema {
	return tools.Schema{
		Name: LoadSkillToolName,
		Description: "Load one of the available skills (built-in or the user's own) by name and return its Markdown instructions. " +
			"The available skills and when to use each are listed under [Available Skills]; load a " +
			"matching skill before starting the task, then follow it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The skill name: lowercase letters, digits, and dashes, e.g. ops-runbook.",
				},
			},
			"required": []any{"name"},
		},
	}
}

// SkillCatalog lists the caller's enabled skills for the prompt.
func (m *Manager) SkillCatalog(ctx context.Context) ([]tools.SkillSummary, error) {
	entries, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	catalog := make([]tools.SkillSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.Disabled {
			continue
		}
		catalog = append(catalog, tools.SkillSummary{Name: entry.Name, Description: entry.Description})
	}
	return catalog, nil
}

// LoadSkill resolves the effective material for the model through the
// dispatch path — never the Settings read path — so a disabled skill fails
// closed here, and the digest in the output pins exactly the material handed
// to the model as dispatch evidence. Owner isolation and the legacy shared
// state resolve from the caller's context and the manager's shared flag, the
// same resolution the IPC dispatch path uses.
func (m *Manager) LoadSkill(ctx context.Context, rawArgs json.RawMessage) tools.Output {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return tools.Fail(fmt.Errorf("%w: %v", ErrInvalidSkill, err))
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return tools.Fail(fmt.Errorf("%w: skill name is required", ErrInvalidSkill))
	}
	dispatch, err := m.Dispatch(ctx, name)
	if err != nil {
		return tools.Fail(err)
	}
	var text strings.Builder
	fmt.Fprintf(&text, "skill: %s\nsource: %s\ndigest: %s\ndescription: %s\n\n%s",
		dispatch.Name, dispatch.Source, dispatch.Digest, dispatch.Description, dispatch.Body)
	return tools.OK(text.String())
}
