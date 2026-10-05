package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type ModelList struct {
	Models    []string `json:"models"`
	Malformed int      `json:"malformed"`
}

func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	list, err := c.ListModelsDetailed(ctx)
	return list.Models, err
}

func (c *Client) ListModelsDetailed(ctx context.Context) (ModelList, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.timeouts.Models)
	defer cancel()
	request, err := c.newRequest(requestContext, http.MethodGet, "/models", nil)
	if err != nil {
		return ModelList{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return ModelList{}, fmt.Errorf("AI model list request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ModelList{}, responseError("AI model list", response)
	}
	var payload struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return ModelList{}, fmt.Errorf("decode AI model list: %w", err)
	}
	models := make([]string, 0)
	if len(payload.Data) == 0 || string(payload.Data) == "null" {
		return ModelList{Models: models}, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(payload.Data, &entries); err != nil {
		return ModelList{}, fmt.Errorf("AI model list: malformed data field: %w", err)
	}
	malformed := 0
	for _, entry := range entries {
		var object struct {
			ID json.RawMessage `json:"id"`
		}
		var id string
		if err := json.Unmarshal(entry, &object); err != nil ||
			json.Unmarshal(object.ID, &id) != nil || strings.TrimSpace(id) == "" {
			malformed++
			continue
		}
		models = append(models, id)
	}
	if len(entries) > 0 && len(models) == 0 {
		return ModelList{Models: models, Malformed: malformed}, fmt.Errorf("AI model list: all %d entries malformed", malformed)
	}
	return ModelList{Models: models, Malformed: malformed}, nil
}

func (c *Client) Test(ctx context.Context) TestResult {
	var result TestResult
	_, err := c.ListModels(ctx)
	setTestError(&result.ModelsOK, &result.ModelsError, err)
	if ctx.Err() != nil {
		setTestError(&result.ChatOK, &result.ChatError, ctx.Err())
		return result
	}
	completion, err := c.ChatBlock(ctx, ChatRequest{Messages: []ChatMessage{UserMessage("ping")}})
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
