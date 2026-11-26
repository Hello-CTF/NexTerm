package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// The invokable tool registered for a name must advertise the same name and
// parameters as the schema the registry publishes for it; enablement is decided
// by that same name.
func TestEinoToolsMatchAdvertisedSchemas(t *testing.T) {
	schemas := map[string]Schema{}
	for _, schema := range einoToolSchemas() {
		if _, dup := schemas[schema.Name]; dup {
			t.Fatalf("schema %s advertised twice", schema.Name)
		}
		schemas[schema.Name] = schema
	}
	if len(schemas) != len(einoTools) {
		t.Fatalf("advertised %d schemas for %d tools", len(schemas), len(einoTools))
	}
	execution := &Execution{}
	for _, current := range einoTools {
		built, err := current.make(execution)
		if err != nil {
			t.Fatalf("%s: %v", current.name, err)
		}
		info, err := built.Info(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", current.name, err)
		}
		schema, ok := schemas[info.Name]
		if !ok || info.Name != current.name {
			t.Fatalf("tool registered as %s builds %s", current.name, info.Name)
		}
		params, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			t.Fatalf("%s: %v", current.name, err)
		}
		var decoded map[string]any
		encoded, _ := json.Marshal(params)
		_ = json.Unmarshal(encoded, &decoded)
		if !reflect.DeepEqual(decoded, schema.Parameters) {
			t.Errorf("%s: invokable parameters differ from the advertised schema", current.name)
		}
	}
}
