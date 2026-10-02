package forward

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestApplicationModuleContract(t *testing.T) {
	service := NewService(Config{
		Provider: &switchProvider{dialer: &recordingDialer{upstream: startEchoServer(t)}},
		Policy:   Policy{Desktop: true},
	})
	application, err := app.New(app.Config{Modules: []app.Module{{
		Name:             "forward",
		RegisterCommands: service.RegisterCommands,
		Component:        service,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	response := application.Dispatcher.Dispatch(t.Context(), ipc.Request{
		Command: "forward_create",
		Args:    json.RawMessage(`{"sessionId":"s","targetHost":"host","targetPort":22}`),
	}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("forward_create through app.Module = %+v", response.Error)
	}
	var spec Spec
	if err := json.Unmarshal(response.Data, &spec); err != nil {
		t.Fatal(err)
	}
	if len(service.List()) != 1 {
		t.Fatal("forward was not registered before application shutdown")
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(service.List()); got != 0 {
		t.Fatalf("forward count after application shutdown = %d", got)
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("listener survived application shutdown")
	}
	if err := service.Start(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("service Start after application shutdown = %v", err)
	}
}
