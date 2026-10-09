package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/mount"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type serverEchoDialer struct {
	upstream string
}

func (d *serverEchoDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", d.upstream)
}

func TestRealForwardMountModulesThroughApplicationAndHTTP(t *testing.T) {
	ctx := context.Background()
	upstream := startServerEcho(t)
	_, portText, err := net.SplitHostPort(upstream)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &serverEchoDialer{upstream: upstream}
	forwardService := forward.NewService(forward.Config{
		Provider: forward.DialerProviderFunc(func(_ context.Context, sessionID string) (base.Dialer, error) {
			if sessionID == "missing" {
				return nil, session.ErrSessionNotFound
			}
			return dialer, nil
		}),
		Policy:    forward.Policy{Desktop: false, Platform: "other"},
		Reconnect: forward.ReconnectPolicy{Attempts: 1},
	})
	mountService := mount.NewService(mount.Config{})
	config := testConfig(t, false)
	_, _, syncService := newRealSyncService(t)
	application, err := app.New(app.Config{
		Logger: testLogger(),
		Modules: []app.Module{
			{Name: "sync", RegisterCommands: syncService.RegisterCommands, Component: syncService},
			{Name: "forward", RegisterCommands: forwardService.RegisterCommands, Component: forwardService},
			{Name: "mount", RegisterCommands: mountService.RegisterCommands, Component: mountService},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })
	registered := application.Dispatcher.Commands()
	for _, command := range []string{
		"sync_link_get", "sync_status", "sync_now", "sync_bundle_read", "sync_bundle_write",
		"forward_env", "forward_create", "forward_create_socks", "forward_list", "forward_remove",
		"mount_capability", "mount_create", "mount_list", "mount_remove",
	} {
		if !slices.Contains(registered, command) {
			t.Fatalf("application dispatcher missing command %s (registered: %v)", command, registered)
		}
	}
	config.Dispatcher = application.Dispatcher
	config.Environment = application.Environment("")
	_, httpServer := newTestHTTP(t, config)

	status, body := postForwardMountRPC(t, httpServer, "forward_env", struct{}{}, false)
	if status != http.StatusOK || !body.OK {
		t.Fatalf("forward_env = %d %+v", status, body)
	}
	var environment forward.Environment
	if err := json.Unmarshal(body.Data, &environment); err != nil {
		t.Fatal(err)
	}
	if !environment.Available || environment.ListenHost != "0.0.0.0" || environment.Platform != "other" {
		t.Fatalf("forward environment = %+v", environment)
	}

	status, body = postForwardMountRPC(t, httpServer, "mount_capability", struct{}{}, false)
	if status != http.StatusOK || !body.OK || string(body.Data) != string(mustJSON(t, mountService.Capability())) {
		t.Fatalf("mount capability = %d %+v", status, body)
	}
	status, body = postForwardMountRPC(t, httpServer, "mount_create", mount.CreateArgs{}, true)
	if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeBadParam {
		t.Fatalf("mount validation = %d %+v", status, body)
	}

	status, body = postForwardMountRPC(t, httpServer, "forward_create_socks", forward.CreateSocksArgs{SessionID: "missing"}, false)
	if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("SOCKS risk = %d %+v", status, body)
	}
	detail := string(mustJSON(t, body.Error.Detail))
	if !strings.Contains(detail, "unauthenticated_exposed_socks") || !strings.Contains(detail, "0.0.0.0") || !strings.Contains(detail, "none") {
		t.Fatalf("SOCKS risk detail = %s", detail)
	}
	status, body = postForwardMountRPC(t, httpServer, "forward_create_socks", forward.CreateSocksArgs{SessionID: "missing", AcknowledgeRisk: true}, false)
	if status != http.StatusOK || body.OK || body.Error == nil || body.Error.Code != ipc.CodeNotFound {
		t.Fatalf("session error through forward IPC = %d %+v", status, body)
	}

	status, body = postForwardMountRPC(t, httpServer, "forward_create", forward.CreateLocalArgs{
		SessionID: "echo", TargetHost: "127.0.0.1", TargetPort: uint16(port),
	}, false)
	if status != http.StatusOK || !body.OK {
		t.Fatalf("forward_create = %d %+v", status, body)
	}
	var spec forward.Spec
	if err := json.Unmarshal(body.Data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.ListenHost != "0.0.0.0" || spec.ListenPort == 0 || len(forwardService.List()) != 1 {
		t.Fatalf("forward spec = %+v list=%+v", spec, forwardService.List())
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("module-echo")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len("module-echo"))
	if _, err := io.ReadFull(connection, echo); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if string(echo) != "module-echo" {
		t.Fatalf("forward echo = %q", echo)
	}

	if err := application.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(forwardService.List()); got != 0 {
		t.Fatalf("forward count after application shutdown = %d", got)
	}
	connection, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("forward listener survived application shutdown")
	}
	if err := forwardService.Start(ctx); !errors.Is(err, forward.ErrClosed) {
		t.Fatalf("forward Start after application shutdown = %v", err)
	}
}

func postForwardMountRPC(t *testing.T, httpServer *httptest.Server, command string, input any, nested bool) (int, ipc.Response) {
	t.Helper()
	args := input
	if nested {
		args = map[string]any{"args": input}
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	rawRequest, err := json.Marshal(ipc.Request{Command: command, Args: rawArgs})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/rpc", strings.NewReader(string(rawRequest)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body ipc.Response
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func startServerEcho(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()
	return listener.Addr().String()
}
