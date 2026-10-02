package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

func toNativeRequest(request ChatRequest) ([]*schema.Message, []model.Option, error) {
	messages := make([]*schema.Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		converted, err := toNativeMessage(message)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, converted)
	}
	if len(request.Tools) == 0 {
		return messages, nil, nil
	}
	tools := make([]*schema.ToolInfo, 0, len(request.Tools))
	for _, tool := range request.Tools {
		converted, err := toNativeTool(tool)
		if err != nil {
			return nil, nil, err
		}
		tools = append(tools, converted)
	}
	return messages, []model.Option{model.WithTools(tools)}, nil
}

func toNativeMessage(message ChatMessage) (*schema.Message, error) {
	converted := &schema.Message{
		Role: schema.RoleType(message.Role), Name: message.Name, ToolCallID: message.ToolCallID,
		ToolCalls: make([]schema.ToolCall, 0, len(message.ToolCalls)),
	}
	for _, call := range message.ToolCalls {
		converted.ToolCalls = append(converted.ToolCalls, schema.ToolCall{
			ID: call.ID, Type: "function",
			Function: schema.FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments},
		})
	}
	if message.Content == nil {
		return converted, nil
	}
	encoded, err := json.Marshal(message.Content)
	if err != nil {
		return nil, fmt.Errorf("encode AI message content: %w", err)
	}
	var text string
	if err := json.Unmarshal(encoded, &text); err == nil {
		converted.Content = text
		return converted, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL    string `json:"url"`
			Detail string `json:"detail"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(encoded, &parts); err != nil {
		return nil, fmt.Errorf("decode AI message content: %w", err)
	}
	converted.UserInputMultiContent = make([]schema.MessageInputPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "", "text":
			converted.UserInputMultiContent = append(converted.UserInputMultiContent, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText, Text: part.Text,
			})
		case "image_url":
			image := schema.MessageInputImage{Detail: schema.ImageURLDetail(part.ImageURL.Detail)}
			url := part.ImageURL.URL
			if metadata, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ","); strings.HasPrefix(url, "data:") && ok && strings.HasSuffix(metadata, ";base64") {
				image.Base64Data = &data
				image.MIMEType = strings.TrimSuffix(metadata, ";base64")
			} else {
				image.URL = &url
			}
			converted.UserInputMultiContent = append(converted.UserInputMultiContent, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeImageURL, Image: &image,
			})
		default:
			return nil, fmt.Errorf("unsupported AI message content part %q", part.Type)
		}
	}
	return converted, nil
}

func toNativeTool(tool ToolSchema) (*schema.ToolInfo, error) {
	converted := &schema.ToolInfo{Name: tool.Name, Desc: tool.Description}
	if tool.Parameters == nil {
		return converted, nil
	}
	encoded, err := json.Marshal(tool.Parameters)
	if err != nil {
		return nil, fmt.Errorf("encode AI tool parameters: %w", err)
	}
	var parameters jsonschema.Schema
	if err := json.Unmarshal(encoded, &parameters); err != nil {
		return nil, fmt.Errorf("decode AI tool parameters: %w", err)
	}
	converted.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&parameters)
	return converted, nil
}
