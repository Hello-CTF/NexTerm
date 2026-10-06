package steer

import (
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestValidateHistoryToolPairs(t *testing.T) {
	assistant := schema.AssistantMessage("", []schema.ToolCall{
		{ID: "a", Function: schema.FunctionCall{Name: "one", Arguments: `{}`}},
		{ID: "b", Function: schema.FunctionCall{Name: "two", Arguments: `{}`}},
	})
	resultA := schema.ToolMessage("a-result", "a")
	resultB := schema.ToolMessage("b-result", "b")
	valid := []*schema.Message{schema.UserMessage("go"), assistant, resultA, resultB, schema.AssistantMessage("done", nil)}
	if err := ValidateHistory(valid); err != nil {
		t.Fatalf("valid history rejected: %v", err)
	}
	cases := map[string][]*schema.Message{
		"missing result": {schema.UserMessage("go"), assistant, resultA},
		"wrong ID":       {schema.UserMessage("go"), assistant, resultA, schema.ToolMessage("wrong", "c")},
		"duplicate":      {schema.UserMessage("go"), assistant, resultA, resultA},
		"orphan result":  {schema.UserMessage("go"), resultA},
		"empty call ID":  {schema.AssistantMessage("", []schema.ToolCall{{Function: schema.FunctionCall{Name: "one"}}}), schema.ToolMessage("x", "")},
	}
	for name, history := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateHistory(history); !errors.Is(err, ErrInvalidHistory) {
				t.Fatalf("ValidateHistory error = %v, want ErrInvalidHistory", err)
			}
		})
	}
}
