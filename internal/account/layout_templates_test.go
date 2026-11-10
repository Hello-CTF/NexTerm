package account

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

const layoutTemplatesKey = "workspace.layoutTemplates"

func layoutLeaf(snippetID string) map[string]any {
	leaf := map[string]any{"type": "leaf"}
	if snippetID != "" {
		leaf["snippetId"] = snippetID
	}
	return leaf
}

func layoutSplit(dir string, ratio float64, a, b any) map[string]any {
	return map[string]any{"type": "split", "dir": dir, "ratio": ratio, "a": a, "b": b}
}

func layoutTemplateValue(id string, layout any) map[string]any {
	return map[string]any{"id": id, "name": "部署 " + id, "assetId": "01HZY3ASSET", "layout": layout}
}

// layoutChain is a split tree of the given depth whose every split holds one leaf and the rest of the chain.
func layoutChain(depth int) any {
	var node any = layoutLeaf("")
	for range depth {
		node = layoutSplit("row", 0.5, layoutLeaf(""), node)
	}
	return node
}

// layoutBalanced is a full binary tree with 2^depth leaves.
func layoutBalanced(depth int) any {
	if depth == 0 {
		return layoutLeaf("")
	}
	return layoutSplit("col", 0.5, layoutBalanced(depth-1), layoutBalanced(depth-1))
}

func encodeLayoutTemplates(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLayoutTemplatesPreference(t *testing.T) {
	want := `[{"id":"lt-1","name":"部署 <prod>","description":"三窗格","icon":"Server","assetId":"01HZY3ASSET","layout":{"type":"split","dir":"row","ratio":0.6,"a":{"type":"leaf","snippetId":"01HZY3SNIP"},"b":{"type":"split","dir":"col","ratio":0.5,"a":{"type":"leaf"},"b":{"type":"leaf"}}}}]`
	got, err := validatePreferenceValue(layoutTemplatesKey, json.RawMessage(want))
	if err != nil || got != want {
		t.Fatalf("layout templates=(%q,%v) want %q", got, err, want)
	}
	if got, err := validatePreferenceValue(layoutTemplatesKey, json.RawMessage(`[]`)); err != nil || got != `[]` {
		t.Fatalf("empty templates=(%q,%v)", got, err)
	}

	accepted := map[string]any{
		"single pane":        []any{layoutTemplateValue("a", layoutLeaf(""))},
		"max panes":          []any{layoutTemplateValue("a", layoutBalanced(4))},
		"max depth":          []any{layoutTemplateValue("a", layoutChain(maxLayoutTemplateDepth))},
		"ratio bounds":       []any{layoutTemplateValue("a", layoutSplit("row", layoutTemplateRatioMin, layoutLeaf(""), layoutSplit("col", layoutTemplateRatioMax, layoutLeaf(""), layoutLeaf(""))))},
		"max name":           []any{map[string]any{"id": "a", "name": strings.Repeat("名", maxLayoutTemplateName), "assetId": "x", "layout": layoutLeaf("")}},
		"max description":    []any{map[string]any{"id": "a", "name": "n", "description": strings.Repeat("述", maxLayoutTemplateDescription), "assetId": "x", "layout": layoutLeaf("")}},
		"max template count": layoutTemplateList(maxLayoutTemplates),
	}
	for name, value := range accepted {
		if _, err := validatePreferenceValue(layoutTemplatesKey, encodeLayoutTemplates(t, value)); err != nil {
			t.Fatalf("%s must be accepted: %v", name, err)
		}
	}

	template := func(mutate func(map[string]any)) any {
		value := layoutTemplateValue("a", layoutSplit("row", 0.5, layoutLeaf(""), layoutLeaf("")))
		mutate(value)
		return []any{value}
	}
	invalid := map[string]json.RawMessage{
		"null":              json.RawMessage(`null`),
		"object":            encodeLayoutTemplates(t, layoutTemplateValue("a", layoutLeaf(""))),
		"too many":          encodeLayoutTemplates(t, layoutTemplateList(maxLayoutTemplates+1)),
		"duplicate id":      encodeLayoutTemplates(t, []any{layoutTemplateValue("a", layoutLeaf("")), layoutTemplateValue("a", layoutLeaf(""))}),
		"bad id":            encodeLayoutTemplates(t, template(func(v map[string]any) { v["id"] = "a b" })),
		"empty name":        encodeLayoutTemplates(t, template(func(v map[string]any) { v["name"] = "  " })),
		"name too long":     encodeLayoutTemplates(t, template(func(v map[string]any) { v["name"] = strings.Repeat("名", maxLayoutTemplateName+1) })),
		"name newline":      encodeLayoutTemplates(t, template(func(v map[string]any) { v["name"] = "a\nb" })),
		"description long":  encodeLayoutTemplates(t, template(func(v map[string]any) { v["description"] = strings.Repeat("述", maxLayoutTemplateDescription+1) })),
		"bad icon":          encodeLayoutTemplates(t, template(func(v map[string]any) { v["icon"] = "<svg>" })),
		"missing asset":     encodeLayoutTemplates(t, template(func(v map[string]any) { delete(v, "assetId") })),
		"bad asset":         encodeLayoutTemplates(t, template(func(v map[string]any) { v["assetId"] = "a/b" })),
		"missing layout":    encodeLayoutTemplates(t, template(func(v map[string]any) { delete(v, "layout") })),
		"too many panes":    encodeLayoutTemplates(t, []any{layoutTemplateValue("a", layoutSplit("row", 0.5, layoutBalanced(4), layoutLeaf("")))}),
		"too deep":          encodeLayoutTemplates(t, []any{layoutTemplateValue("a", layoutChain(maxLayoutTemplateDepth+1))}),
		"ratio low":         encodeLayoutTemplates(t, []any{layoutTemplateValue("a", layoutSplit("row", 0.1, layoutLeaf(""), layoutLeaf("")))}),
		"ratio high":        encodeLayoutTemplates(t, []any{layoutTemplateValue("a", layoutSplit("row", 0.9, layoutLeaf(""), layoutLeaf("")))}),
		"missing ratio":     encodeLayoutTemplates(t, []any{layoutTemplateValue("a", map[string]any{"type": "split", "dir": "row", "a": layoutLeaf(""), "b": layoutLeaf("")})}),
		"bad direction":     encodeLayoutTemplates(t, []any{layoutTemplateValue("a", layoutSplit("diagonal", 0.5, layoutLeaf(""), layoutLeaf("")))}),
		"missing child":     encodeLayoutTemplates(t, []any{layoutTemplateValue("a", map[string]any{"type": "split", "dir": "row", "ratio": 0.5, "a": layoutLeaf("")})}),
		"leaf with child":   encodeLayoutTemplates(t, []any{layoutTemplateValue("a", map[string]any{"type": "leaf", "a": layoutLeaf("")})}),
		"split snippet":     encodeLayoutTemplates(t, []any{layoutTemplateValue("a", map[string]any{"type": "split", "dir": "row", "ratio": 0.5, "snippetId": "s", "a": layoutLeaf(""), "b": layoutLeaf("")})}),
		"bad snippet":       encodeLayoutTemplates(t, []any{layoutTemplateValue("a", layoutLeaf("s n"))}),
		"unknown node type": encodeLayoutTemplates(t, []any{layoutTemplateValue("a", map[string]any{"type": "tab"})}),
		"trailing values":   json.RawMessage(`[] []`),
		"list too large":    encodeLayoutTemplates(t, bulkyLayoutTemplates()),
	}
	for name, raw := range invalid {
		if got, err := validatePreferenceValue(layoutTemplatesKey, raw); err == nil {
			t.Fatalf("%s accepted as %q", name, got)
		}
	}
}

func layoutTemplateList(count int) []any {
	list := make([]any, count)
	for i := range list {
		list[i] = layoutTemplateValue("t"+strconv.Itoa(i), layoutLeaf(""))
	}
	return list
}

// bulkyLayoutTemplates stays within every per-template limit while the list exceeds the total size limit.
func bulkyLayoutTemplates() []any {
	list := make([]any, maxLayoutTemplates)
	for i := range list {
		list[i] = map[string]any{
			"id":          "t" + strconv.Itoa(i),
			"name":        strings.Repeat("名", maxLayoutTemplateName),
			"description": strings.Repeat("述", maxLayoutTemplateDescription),
			"assetId":     strings.Repeat("a", 128),
			"layout":      layoutBalanced(4),
		}
	}
	return list
}

func TestLayoutTemplatesRoundTripPerUser(t *testing.T) {
	ctx := context.Background()
	preferences := testPreferences(t)
	value := json.RawMessage(`[{"id":"lt-1","name":"日志","assetId":"01HZY3ASSET","layout":{"type":"leaf","snippetId":"01HZY3SNIP"}}]`)
	updated, err := preferences.UpdateUserOverrides(ctx, "alice", map[string]json.RawMessage{layoutTemplatesKey: value}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated[layoutTemplatesKey]) != string(value) {
		t.Fatalf("stored overrides=%v", updated)
	}
	if _, err := preferences.UpdateUserOverrides(ctx, "alice", map[string]json.RawMessage{
		layoutTemplatesKey: json.RawMessage(`[{"id":"lt-1","name":"","assetId":"x","layout":{"type":"leaf"}}]`),
	}, nil); err == nil {
		t.Fatal("an invalid template list must be rejected")
	}
	read, err := preferences.UserOverrides(ctx, "alice")
	if err != nil || string(read[layoutTemplatesKey]) != string(value) {
		t.Fatalf("rejected write must keep the stored list: %v %v", read, err)
	}
	other, err := preferences.UserOverrides(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := other[layoutTemplatesKey]; ok {
		t.Fatalf("another account must not see alice's templates: %v", other)
	}
	if _, err := preferences.UpdateUserOverrides(ctx, "alice", nil, []string{layoutTemplatesKey}); err != nil {
		t.Fatal(err)
	}
	read, err = preferences.UserOverrides(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := read[layoutTemplatesKey]; ok {
		t.Fatalf("cleared templates must be gone: %v", read)
	}
}

func TestNormalizeLayoutTemplatesDropsUnknownFields(t *testing.T) {
	leaf := layoutLeaf("snip")
	leaf["sessionId"] = "live"
	value := layoutTemplateValue("a", layoutSplit("row", 0.5, leaf, layoutLeaf("")))
	value["run"] = "rm -rf /"
	raw := encodeLayoutTemplates(t, []any{value})
	got, err := validatePreferenceValue(layoutTemplatesKey, raw)
	if err != nil {
		t.Fatalf("template with unknown fields rejected: %v", err)
	}
	for _, key := range []string{"run", "sessionId", "rm -rf"} {
		if strings.Contains(string(got), key) {
			t.Fatalf("unknown field %q kept in %s", key, got)
		}
	}
	if !strings.Contains(string(got), `"snippetId":"snip"`) {
		t.Fatalf("known fields lost: %s", got)
	}
}
