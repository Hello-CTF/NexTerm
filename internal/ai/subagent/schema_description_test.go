package subagent

import (
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/components/tool/utils"
)

func assertDescribed[T any](t *testing.T, name string) {
	t.Helper()
	info, err := utils.GoStruct2ToolInfo[T](name, name)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var parameters struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	encoded, _ := json.Marshal(converted)
	if err := json.Unmarshal(encoded, &parameters); err != nil {
		t.Fatal(err)
	}
	if len(parameters.Properties) == 0 {
		t.Fatalf("%s: no properties", name)
	}
	for property, schema := range parameters.Properties {
		if schema.Description == "" {
			t.Errorf("%s: parameter %s has no description", name, property)
		}
	}
}

func TestToolParametersAreDescribed(t *testing.T) {
	assertDescribed[SpawnInput](t, "SpawnInput")
}
