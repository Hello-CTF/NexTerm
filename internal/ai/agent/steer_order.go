package agent

import "github.com/cloudwego/eino/schema"

// trailingToolResults returns the call IDs of the tool results that end the
// messages: the results of the last assistant tool call, which the model sees
// right before a steer delivered now.
func trailingToolResults(messages []*schema.Message) []string {
	var ids []string
	for index := len(messages) - 1; index >= 0 && messages[index] != nil && messages[index].Role == schema.Tool; index-- {
		if messages[index].ToolCallID != "" {
			ids = append(ids, messages[index].ToolCallID)
		}
	}
	return ids
}
