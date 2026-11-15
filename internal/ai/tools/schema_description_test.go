package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// missingDescriptions walks an object schema and returns the property paths
// that carry no description.
func missingDescriptions(path string, node map[string]any) []string {
	var missing []string
	properties, _ := node["properties"].(map[string]any)
	for name, raw := range properties {
		child, _ := raw.(map[string]any)
		childPath := path + "." + name
		if text, _ := child["description"].(string); text == "" {
			missing = append(missing, childPath)
		}
		missing = append(missing, missingDescriptions(childPath, child)...)
		if items, ok := child["items"].(map[string]any); ok {
			missing = append(missing, missingDescriptions(childPath+"[]", items)...)
		}
	}
	return missing
}

func TestEveryToolParameterHasDescription(t *testing.T) {
	for _, schema := range einoToolSchemas() {
		if missing := missingDescriptions(schema.Name, schema.Parameters); len(missing) > 0 {
			t.Errorf("%s: parameters without description: %v", schema.Name, missing)
		}
	}
}

func TestToolParameterEnumsAndRequired(t *testing.T) {
	enums := map[string]map[string][]any{
		"search_files":   {"by": {"name", "content"}},
		"docker_control": {"action": {"start", "stop", "restart", "rm"}},
		"wait_for":       {"mode": {"any", "all"}},
	}
	required := map[string][]string{
		"exec_commands":   {"commands"},
		"read_file":       {"path"},
		"edit_file":       {"path", "old_string", "new_string"},
		"search_files":    {"path", "pattern", "by"},
		"docker_logs":     {"container_id"},
		"db_describe":     {"table"},
		"send_keys":       {"keys"},
		"wait_for":        nil,
		"read_screen":     nil,
		"redis_scan":      nil,
		"terminal_list":   nil,
		"shell_history":   nil,
		"list_assets":     nil,
		"docker_ps":       nil,
		"terminal_create": nil,
	}
	byName := map[string]Schema{}
	for _, schema := range einoToolSchemas() {
		byName[schema.Name] = schema
	}
	for tool, fields := range enums {
		properties, _ := byName[tool].Parameters["properties"].(map[string]any)
		for field, want := range fields {
			property, _ := properties[field].(map[string]any)
			got, _ := property["enum"].([]any)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s.%s enum = %v, want %v", tool, field, got, want)
			}
		}
	}
	todos, _ := byName["todo_write"].Parameters["properties"].(map[string]any)["todos"].(map[string]any)
	item, _ := todos["items"].(map[string]any)
	status, _ := item["properties"].(map[string]any)["status"].(map[string]any)
	if got, _ := status["enum"].([]any); !reflect.DeepEqual(got, []any{"pending", "in_progress", "completed"}) {
		t.Errorf("todo status enum = %v", got)
	}
	for tool, want := range required {
		var got []string
		if list, ok := byName[tool].Parameters["required"].([]any); ok {
			for _, name := range list {
				got = append(got, name.(string))
			}
		}
		slices.Sort(got)
		want = slices.Clone(want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s required = %v, want %v", tool, got, want)
		}
	}
}

// No-argument tools keep an explicit empty object schema: the OpenAI wire
// format would otherwise send "parameters": null, which gateways reject.
func TestNoArgumentToolsKeepEmptyObjectSchema(t *testing.T) {
	seen := 0
	for _, schema := range einoToolSchemas() {
		if schema.Name != "docker_ps" && schema.Name != "list_assets" {
			continue
		}
		seen++
		if schema.Parameters["type"] != "object" {
			t.Errorf("%s parameters type = %v", schema.Name, schema.Parameters["type"])
		}
		if properties, _ := schema.Parameters["properties"].(map[string]any); len(properties) != 0 {
			t.Errorf("%s properties = %v", schema.Name, properties)
		}
	}
	if seen != 2 {
		t.Fatalf("no-argument tools found = %d", seen)
	}
}

// The tools handed to the model are inferred from the same argument structs,
// so each must describe its parameters too.
func TestExecutionToolInfosDescribeParameters(t *testing.T) {
	execution := &Execution{JobID: "j", Registry: NewRegistry(Dependencies{Terminal: &fakeTerminal{}}), Scope: Scope{SessionID: "s", TabID: "t"}}
	einoTools, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	if len(einoTools) == 0 {
		t.Fatal("no tools exposed")
	}
	for _, current := range einoTools {
		info, err := current.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		converted, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			t.Fatal(err)
		}
		var parameters map[string]any
		encoded, _ := json.Marshal(converted)
		_ = json.Unmarshal(encoded, &parameters)
		if missing := missingDescriptions(info.Name, parameters); len(missing) > 0 {
			t.Errorf("%s: parameters without description: %v", info.Name, missing)
		}
	}
}
