package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testDispatcher(t *testing.T) *ipc.Dispatcher {
	t.Helper()
	dispatcher := ipc.NewDispatcher()
	for _, command := range SyncOnlyCommands() {
		command := command
		if err := dispatcher.RegisterRaw(command, func(context.Context, *ipc.Call) (any, error) {
			return map[string]string{"command": command}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"app_info", "app_platform", "terminal_attach", "sync_push", "sync_pull", "sync_link_set"} {
		command := command
		if err := dispatcher.RegisterRaw(command, func(context.Context, *ipc.Call) (any, error) {
			return map[string]string{"command": command}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dispatcher.RegisterRaw("client_id", func(_ context.Context, call *ipc.Call) (any, error) {
		return call.ClientID, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.RegisterRaw("null_result", func(context.Context, *ipc.Call) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.RegisterRaw("fail", func(context.Context, *ipc.Call) (any, error) {
		return nil, ipc.NewError(ipc.CodeVaultLocked, "locked").WithDetail(map[string]bool{"retryable": true})
	}); err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func unavailableChannels() ChannelBinder {
	return ChannelBinderFunc(func(string) (ChannelReceiver, error) {
		return nil, errors.New("no test channel")
	})
}

func testConfig(t *testing.T, syncOnly bool) Config {
	t.Helper()
	return Config{
		Options: Options{
			Listen: "127.0.0.1:0", DataDir: t.TempDir(), SyncOnly: syncOnly,
		},
		Dispatcher: testDispatcher(t),
		Tokens: TokenVerifierFunc(func(_ context.Context, token string) (bool, error) {
			return token == "secret", nil
		}),
		Channels: unavailableChannels(),
		Version:  "test-version",
		Logger:   testLogger(),
	}
}

func newTestHTTP(t *testing.T, config Config) (*Server, *httptest.Server) {
	t.Helper()
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = server.Close()
	})
	return server, httpServer
}

func postRPC(t *testing.T, client *http.Client, url, command string, headers map[string]string) (int, ipc.Response) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"cmd":`+strconv.Quote(command)+`,"args":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
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
