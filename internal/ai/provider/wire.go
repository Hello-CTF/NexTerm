package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
)

type wireTransport struct {
	base http.RoundTripper
}

func (t *wireTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	state := attemptFromContext(request.Context())
	if state == nil || !strings.HasSuffix(request.URL.Path, "/chat/completions") {
		return t.base.RoundTrip(request)
	}
	normalized, err := normalizeChatRequest(request)
	if err != nil {
		return nil, err
	}
	response, err := t.base.RoundTrip(normalized)
	if err != nil || response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	if state.streaming {
		response.Body = newSSEBody(response.Body, state)
		return response, nil
	}
	body, err := normalizeBlockBody(response.Body, state)
	if err != nil {
		response.Body.Close()
		return nil, fmt.Errorf("read AI block response: %w", err)
	}
	response.Body = body
	return response, nil
}

func normalizeChatRequest(request *http.Request) (*http.Request, error) {
	if request.Body == nil {
		return request, nil
	}
	raw, err := io.ReadAll(request.Body)
	request.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read AI request for normalization: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("decode AI request for normalization: %w", err)
	}
	var messages []map[string]json.RawMessage
	if rawMessages := object["messages"]; len(rawMessages) != 0 && json.Unmarshal(rawMessages, &messages) == nil {
		changed := false
		for _, message := range messages {
			role := ""
			_ = json.Unmarshal(message["role"], &role)
			if role != "assistant" {
				continue
			}
			if _, exists := message["content"]; exists {
				continue
			}
			if _, hasTools := message["tool_calls"]; hasTools {
				message["content"] = json.RawMessage("null")
			} else {
				message["content"] = json.RawMessage(`""`)
			}
			changed = true
		}
		if changed {
			object["messages"], _ = json.Marshal(messages)
		}
	}
	if wantsToolStream(request.URL.String()) {
		var stream bool
		_ = json.Unmarshal(object["stream"], &stream)
		var tools []any
		if stream && json.Unmarshal(object["tools"], &tools) == nil && len(tools) != 0 {
			object["tool_stream"] = json.RawMessage("true")
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode normalized AI request: %w", err)
	}
	cloned := request.Clone(request.Context())
	cloned.Body = io.NopCloser(bytes.NewReader(encoded))
	cloned.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}
	cloned.ContentLength = int64(len(encoded))
	return cloned, nil
}

func normalizeBlockBody(body io.ReadCloser, state *attemptState) (io.ReadCloser, error) {
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(canonicalizePayload(raw, state))), nil
}

func canonicalizePayload(raw []byte, state *attemptState) []byte {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return raw
	}
	var servingModel string
	if json.Unmarshal(object["model"], &servingModel) == nil {
		state.setServingModel(strings.TrimSpace(servingModel))
	}
	if rawUsage := object["usage"]; len(rawUsage) != 0 && string(rawUsage) != "null" {
		object["usage"] = canonicalizeUsage(rawUsage)
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return encoded
}

func canonicalizeUsage(raw json.RawMessage) json.RawMessage {
	parsed := usage.Parse(raw)
	if !parsed.HasData() {
		return raw
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return raw
	}
	object["prompt_tokens"], _ = json.Marshal(parsed.PromptTokens)
	object["completion_tokens"], _ = json.Marshal(parsed.CompletionTokens)
	if parsed.CachedTokens != 0 {
		var details map[string]json.RawMessage
		if rawDetails := object["prompt_tokens_details"]; len(rawDetails) != 0 {
			_ = json.Unmarshal(rawDetails, &details)
		}
		if details == nil {
			details = make(map[string]json.RawMessage)
		}
		details["cached_tokens"], _ = json.Marshal(parsed.CachedTokens)
		object["prompt_tokens_details"], _ = json.Marshal(details)
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return encoded
}

type sseBody struct {
	reader   *bufio.Reader
	closer   io.Closer
	state    *attemptState
	pending  bytes.Buffer
	terminal error
}

func newSSEBody(body io.ReadCloser, state *attemptState) io.ReadCloser {
	return &sseBody{reader: bufio.NewReader(body), closer: body, state: state}
}

func (b *sseBody) Read(target []byte) (int, error) {
	for b.pending.Len() == 0 {
		if b.terminal != nil {
			return 0, b.terminal
		}
		data, found, err := readSSEData(b.reader)
		if found {
			b.state.frameSeen.Store(true)
			trimmed := strings.TrimSpace(data)
			switch {
			case trimmed == "":
			case trimmed == "[DONE]":
				b.pending.WriteString("data: [DONE]\n\n")
			default:
				b.pending.WriteString("data: ")
				b.pending.Write(canonicalizePayload([]byte(data), b.state))
				b.pending.WriteString("\n\n")
			}
		}
		if err != nil {
			b.terminal = err
		}
	}
	return b.pending.Read(target)
}

func (b *sseBody) Close() error {
	return b.closer.Close()
}

func readSSEData(reader *bufio.Reader) (string, bool, error) {
	var dataLines []string
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) != 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if line == "" {
				if len(dataLines) != 0 {
					return strings.Join(dataLines, "\n"), true, readErr
				}
			} else if !strings.HasPrefix(line, ":") {
				field, value, found := strings.Cut(line, ":")
				if !found {
					field = line
					value = ""
				}
				if field == "data" {
					dataLines = append(dataLines, strings.TrimPrefix(value, " "))
				}
			}
		}
		if readErr != nil {
			if len(dataLines) != 0 {
				return strings.Join(dataLines, "\n"), true, readErr
			}
			return "", false, readErr
		}
	}
}

func wantsToolStream(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if domainMatch(host, "bigmodel.cn") || domainMatch(host, "z.ai") {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "zhipuai" {
			return true
		}
	}
	return false
}

func domainMatch(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}
