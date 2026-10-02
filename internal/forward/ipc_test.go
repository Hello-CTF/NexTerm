package forward

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestIPCCommands(t *testing.T) {
	service := NewService(Config{
		Provider: &switchProvider{dialer: &recordingDialer{upstream: startEchoServer(t)}},
		Policy:   Policy{},
	})
	service.listen = func(_, _ string) (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:0")
	}
	t.Cleanup(func() { _ = service.Close() })
	dispatcher := ipc.NewDispatcher()
	if err := service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	dispatch := func(command string, args string) ipc.Response {
		return dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}
	if response := dispatch("forward_env", `{}`); !response.OK {
		t.Fatalf("forward_env = %+v", response.Error)
	}
	response := dispatch("forward_create_socks", `{"sessionId":"s"}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("risk response = %+v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	response = dispatch("forward_create_socks", `{"sessionId":"s","acknowledgeRisk":true}`)
	if !response.OK {
		t.Fatalf("create = %+v", response.Error)
	}
	var spec Spec
	if err := json.Unmarshal(response.Data, &spec); err != nil {
		t.Fatal(err)
	}
	listResponse := dispatch("forward_list", `{}`)
	var listed []Spec
	if !listResponse.OK || json.Unmarshal(listResponse.Data, &listed) != nil || len(listed) != 1 {
		t.Fatalf("list = %+v", listResponse)
	}
	if response := dispatch("forward_remove", `{"id":"`+spec.ID+`"}`); !response.OK || string(response.Data) != "null" {
		t.Fatalf("remove = %+v", response)
	}
	listResponse = dispatch("forward_list", `{}`)
	listed = nil
	if !listResponse.OK || json.Unmarshal(listResponse.Data, &listed) != nil || len(listed) != 0 {
		t.Fatalf("empty list = %+v", listResponse)
	}
}
