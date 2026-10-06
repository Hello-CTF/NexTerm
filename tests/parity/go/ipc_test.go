package parity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type echoInput struct {
	Value int `json:"value"`
}

type echoOutput struct {
	Value    int    `json:"value"`
	ClientID string `json:"clientId"`
}

func TestCommandRegistrationAndDispatchSelfConsistency(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	if err := ipc.RegisterNested(dispatcher, "self.nested", func(_ context.Context, call *ipc.Call, input echoInput) (echoOutput, error) {
		return echoOutput{Value: input.Value + 1, ClientID: call.ClientID}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.RegisterRaw("self.raw", func(_ context.Context, call *ipc.Call) (any, error) {
		return map[string]any{"command": call.Command}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.RegisterRaw("self.fail", func(context.Context, *ipc.Call) (any, error) {
		return nil, ipc.NewError(ipc.CodeBadParam, "synthetic failure").WithDetail(map[string]any{"field": "value"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.RegisterRaw("self.panic", func(context.Context, *ipc.Call) (any, error) {
		panic("synthetic panic")
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.RegisterRaw("self.raw", func(context.Context, *ipc.Call) (any, error) { return nil, nil }); err == nil {
		t.Fatal("duplicate command registration must fail")
	}
	if err := dispatcher.RegisterRaw("", func(context.Context, *ipc.Call) (any, error) { return nil, nil }); err == nil {
		t.Fatal("empty command name must fail")
	}
	if err := dispatcher.RegisterRaw("self.nil", nil); err == nil {
		t.Fatal("nil command handler must fail")
	}
	if got, want := dispatcher.Commands(), []string{"self.fail", "self.nested", "self.panic", "self.raw"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commands are not sorted and complete: got %v want %v", got, want)
	}

	response := dispatcher.Dispatch(context.Background(), ipc.Request{
		Command:  "self.nested",
		Args:     json.RawMessage(`{"args":{"value":3}}`),
		ClientID: "request-client",
	}, ipc.Environment{ClientID: "environment-client"})
	if !response.OK {
		t.Fatalf("nested dispatch failed: %+v", response.Error)
	}
	var output echoOutput
	if err := json.Unmarshal(response.Data, &output); err != nil {
		t.Fatal(err)
	}
	if output.Value != 4 || output.ClientID != "environment-client" {
		t.Fatalf("unexpected nested result: %+v", output)
	}

	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "self.missing"}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("unknown command must return not_found: %+v", response)
	}
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "self.fail"}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam || response.Error.Detail == nil {
		t.Fatalf("structured command error lost its code/detail: %+v", response)
	}
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "self.panic"}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeInternal {
		t.Fatalf("panic must be contained as internal error: %+v", response)
	}
	response = dispatcher.Dispatch(context.Background(), ipc.Request{Command: "self.raw"}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("dispatcher did not recover after panic: %+v", response.Error)
	}
}

func TestHTTPCycleAndVoidResult(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	if err := ipc.Register(dispatcher, "self.void", func(context.Context, *ipc.Call, struct{}) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ipc.NewRPCHandler(dispatcher, ipc.Environment{}))
	defer server.Close()

	post := func(body string) (int, map[string]any) {
		t.Helper()
		response, err := http.Post(server.URL, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var decoded map[string]any
		if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, decoded
	}
	_, body := post(`{"cmd":"self.void","args":null}`)
	if ok, exists := body["ok"]; !exists || ok != true {
		t.Fatalf("void command did not return ok=true: %v", body)
	}
	if data, exists := body["data"]; !exists || data != nil {
		t.Fatalf("void command must preserve explicit JSON null data: %v", body)
	}
	_, body = post(`{"cmd":"self.missing"} {"cmd":"self.void"}`)
	if errorValue, ok := body["error"].(map[string]any); !ok || errorValue["code"] != string(ipc.CodeBadParam) {
		t.Fatalf("trailing JSON must be rejected: %v", body)
	}
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /rpc status = %d, want 405", response.StatusCode)
	}
}

func TestHTTPChannelDecoding(t *testing.T) {
	dispatcher := ipc.NewDispatcher()
	handled := make([]string, 0, 2)
	if err := dispatcher.RegisterRaw("self.channel", func(_ context.Context, call *ipc.Call) (any, error) {
		handled = append(handled, call.Channel.ID)
		return map[string]any{"channel": call.Channel.ID}, nil
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(ipc.NewRPCHandler(dispatcher, ipc.Environment{}))
	defer server.Close()

	post := func(body string) map[string]any {
		t.Helper()
		response, err := http.Post(server.URL, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var decoded map[string]any
		if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	body := post(`{"cmd":"self.channel","channel":"abc"}`)
	if ok := body["ok"]; ok != true {
		t.Fatalf("string channel rejected: %v", body)
	}
	data, ok := body["data"].(map[string]any)
	if !ok || data["channel"] != "abc" {
		t.Fatalf("string channel did not reach handler: %v", body)
	}
	body = post(`{"cmd":"self.channel","channel":""}`)
	if ok := body["ok"]; ok != true {
		t.Fatalf("empty-string channel rejected: %v", body)
	}
	if len(handled) != 2 || handled[0] != "abc" || handled[1] != "" {
		t.Fatalf("handler saw channels %v, want [abc ]", handled)
	}
	for _, input := range []string{
		`{"cmd":"self.channel","channel":42}`,
		`{"cmd":"self.channel","channel":{"id":"abc"}}`,
		`{"cmd":"self.channel","channel":{"channelId":"abc"}}`,
	} {
		body = post(input)
		errorValue, ok := body["error"].(map[string]any)
		if !ok || errorValue["code"] != string(ipc.CodeBadParam) {
			t.Fatalf("%s must return bad_param: %v", input, body)
		}
	}
	if len(handled) != 2 {
		t.Fatalf("rejected shapes must not reach the handler: %v", handled)
	}
}

type binaryCollector struct {
	frames [][]byte
	closed bool
}

func (c *binaryCollector) SendBinary(_ context.Context, data []byte) error {
	c.frames = append(c.frames, append([]byte(nil), data...))
	return nil
}

func (c *binaryCollector) Close() error {
	c.closed = true
	return nil
}

type jsonCollector struct {
	frames []json.RawMessage
	closed bool
}

func (c *jsonCollector) SendJSON(_ context.Context, data json.RawMessage) error {
	c.frames = append(c.frames, append(json.RawMessage(nil), data...))
	return nil
}

func (c *jsonCollector) Close() error {
	c.closed = true
	return nil
}

type streamMessage struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

func TestEventsAndStreamsSelfConsistency(t *testing.T) {
	topics := []ipc.Topic{
		ipc.TopicSessionStatus,
		ipc.TopicTerminalExit,
		ipc.TopicTerminalThrottled,
		ipc.TopicTerminalControl,
		ipc.TopicFSProgress,
		ipc.TopicDockerStats,
		ipc.TopicAIEvent,
		ipc.TopicSyncStatus,
		ipc.TopicAppError,
		ipc.TopicLayoutChanged,
	}
	seen := map[ipc.Topic]bool{}
	for _, topic := range topics {
		if topic == "" || seen[topic] {
			t.Fatalf("empty or duplicate topic: %q", topic)
		}
		seen[topic] = true
	}
	if err := ipc.Emit(context.Background(), nil, ipc.TopicAppError, map[string]any{}); !errors.Is(err, ipc.ErrEventsUnavailable) {
		t.Fatalf("nil event adapter error = %v", err)
	}
	var events []ipc.Event
	emitter := ipc.EmitterFunc(func(_ context.Context, event ipc.Event) error {
		events = append(events, event)
		return nil
	})
	if err := ipc.Emit(context.Background(), emitter, ipc.TopicSessionStatus, map[string]any{"status": "connected"}); err != nil {
		t.Fatal(err)
	}
	if err := ipc.Emit(context.Background(), emitter, ipc.TopicLayoutChanged, map[string]any{"revision": 1}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Event != ipc.TopicSessionStatus || events[1].Event != ipc.TopicLayoutChanged {
		t.Fatalf("event order/topics changed: %+v", events)
	}

	binary := &binaryCollector{}
	jsonOutput := &jsonCollector{}
	factory := ipc.StreamFactoryFuncs{
		Binary: func(context.Context, ipc.ChannelRef) (ipc.BinaryStream, error) { return binary, nil },
		JSON:   func(context.Context, ipc.ChannelRef) (ipc.JSONStream, error) { return jsonOutput, nil },
	}
	binaryStream, err := factory.OpenBinary(context.Background(), ipc.ChannelRef{ID: "binary"})
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range [][]byte{{0, 1, 127, 128, 255}, []byte("second")} {
		if err := binaryStream.SendBinary(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := binaryStream.Close(); err != nil {
		t.Fatal(err)
	}
	if len(binary.frames) != 2 || !bytes.Equal(binary.frames[0], []byte{0, 1, 127, 128, 255}) || string(binary.frames[1]) != "second" || !binary.closed {
		t.Fatalf("binary stream lost order/bytes/close: %+v", binary)
	}

	typed, err := ipc.OpenTypedJSONStream[streamMessage](context.Background(), factory, ipc.ChannelRef{ID: "json"})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []streamMessage{{Type: "delta", Text: "中文", Done: false}, {Type: "done", Done: true}} {
		if err := typed.Send(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if err := typed.Close(); err != nil {
		t.Fatal(err)
	}
	if len(jsonOutput.frames) != 2 || !jsonOutput.closed {
		t.Fatalf("JSON stream lost frames/close: %+v", jsonOutput)
	}
	var first, second streamMessage
	if err := json.Unmarshal(jsonOutput.frames[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jsonOutput.frames[1], &second); err != nil {
		t.Fatal(err)
	}
	if first.Text != "中文" || first.Done || second.Type != "done" || !second.Done {
		t.Fatalf("typed JSON changed data/order: %+v %+v", first, second)
	}
	if _, err := ipc.OpenTypedJSONStream[streamMessage](context.Background(), nil, ipc.ChannelRef{}); !errors.Is(err, ipc.ErrStreamsUnavailable) {
		t.Fatalf("nil stream adapter error = %v", err)
	}
}

func TestChannelRefAcceptedForms(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{`"abc"`, "abc"},
		{`""`, ""},
		{`null`, ""},
	} {
		var ref ipc.ChannelRef
		if err := json.Unmarshal([]byte(test.input), &ref); err != nil {
			t.Fatalf("%s: %v", test.input, err)
		}
		if ref.ID != test.want {
			t.Fatalf("%s produced %q, want %q", test.input, ref.ID, test.want)
		}
	}
	for _, input := range []string{`42`, `{"id":"nested"}`, `{"channelId":7}`, `true`} {
		var invalid ipc.ChannelRef
		if err := json.Unmarshal([]byte(input), &invalid); err == nil {
			t.Fatalf("%s must fail, got ID %q", input, invalid.ID)
		}
	}
}
