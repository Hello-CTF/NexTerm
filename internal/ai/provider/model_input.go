package provider

import (
	"encoding/json"

	"github.com/cloudwego/eino/schema"
)

type modelResultView struct {
	Tool      string
	OK        bool
	Text      string
	ModelText string
	Truncated bool
	Panic     bool
}

func ProjectModelOutput(tool string, ok bool, text string, truncated, panic bool) string {
	return projectModelResult(modelResultView{Tool: tool, OK: ok, Text: text, Truncated: truncated, Panic: panic})
}

func ProjectModelErrorText(text string) string {
	return projectMachineText(text)
}

func ProjectModelMessages(input []*schema.Message) []*schema.Message {
	if input == nil {
		return nil
	}
	toolNames := make(map[string]string)
	for _, message := range input {
		if message == nil {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID != "" && call.Function.Name != "" {
				toolNames[call.ID] = call.Function.Name
			}
		}
	}
	projected := make([]*schema.Message, len(input))
	for index, message := range input {
		if message == nil {
			continue
		}
		cloned := *message
		projected[index] = &cloned
		if message.Role != schema.Tool {
			continue
		}
		toolName := message.ToolName
		if toolName == "" {
			toolName = toolNames[message.ToolCallID]
		}
		if content, ok := projectModelResultJSON(message.Content, toolName); ok {
			cloned.Content = content
		}
	}
	return projected
}

func projectModelResultJSON(content, toolName string) (string, bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &envelope); err != nil || envelope == nil {
		return content, false
	}
	recognized := false
	for _, key := range []string{"ok", "exitCode", "truncated"} {
		if _, exists := envelope[key]; exists {
			recognized = true
			break
		}
	}
	if !recognized {
		return content, false
	}
	textRaw, exists := envelope["text"]
	if !exists {
		return content, false
	}
	var text *string
	if err := json.Unmarshal(textRaw, &text); err != nil || text == nil {
		return content, false
	}
	view := modelResultView{Tool: toolName, Text: *text}
	if raw, exists := envelope["ok"]; exists {
		if err := json.Unmarshal(raw, &view.OK); err != nil {
			return content, false
		}
	}
	if raw, exists := envelope["truncated"]; exists {
		if err := json.Unmarshal(raw, &view.Truncated); err != nil {
			return content, false
		}
	}
	if raw, exists := envelope["panic"]; exists {
		if err := json.Unmarshal(raw, &view.Panic); err != nil {
			return content, false
		}
	}
	if modelTextRaw, exists := envelope["modelText"]; exists {
		var modelText *string
		if err := json.Unmarshal(modelTextRaw, &modelText); err != nil || modelText == nil {
			return content, false
		}
		view.ModelText = *modelText
	}
	delete(envelope, "modelText")
	view.Text = projectModelResult(view)
	encodedText, err := json.Marshal(view.Text)
	if err != nil {
		return content, false
	}
	envelope["text"] = encodedText
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return content, false
	}
	return string(encoded), true
}
