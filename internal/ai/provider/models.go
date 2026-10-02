package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.timeouts.Models)
	defer cancel()
	request, err := c.newRequest(requestContext, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("AI model list request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, responseError("AI model list", response)
	}
	var payload struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode AI model list: %w", err)
	}
	models := make([]string, 0)
	if len(payload.Data) == 0 {
		return models, nil
	}
	var entries []struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(payload.Data, &entries); err != nil {
		return models, nil
	}
	for _, entry := range entries {
		var id string
		if err := json.Unmarshal(entry.ID, &id); err == nil {
			models = append(models, id)
		}
	}
	return models, nil
}

func (c *Client) Test(ctx context.Context) TestResult {
	var result TestResult
	_, err := c.ListModels(ctx)
	setTestError(&result.ModelsOK, &result.ModelsError, err)
	if ctx.Err() != nil {
		setTestError(&result.ChatOK, &result.ChatError, ctx.Err())
		return result
	}
	completion, err := c.chatBlock(ctx, ChatRequest{Messages: []ChatMessage{UserMessage("ping")}}, nil)
	if err == nil && completion.Content == "" && len(completion.ToolCalls) == 0 {
		err = fmt.Errorf("model returned an empty response")
	}
	setTestError(&result.ChatOK, &result.ChatError, err)
	return result
}

func setTestError(ok *bool, target **string, err error) {
	*ok = err == nil
	if err != nil {
		message := err.Error()
		*target = &message
	}
}
