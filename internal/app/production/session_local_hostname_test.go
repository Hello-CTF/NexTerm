//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestProductionSessionLocalHostname(t *testing.T) {
	factory := &bridgeTestFactory{}
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
		},
		DataDir:   t.TempDir(),
		Desktop:   true,
		Connector: fakeSSHConnector{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	local := connectLocalTerminalTest(t, production)
	response := dispatchDurableTest(t, production, "session_local_hostname", `{"sessionId":"`+local.ID+`"}`, "", "")
	var name string
	requireStoreTestResponse(t, response, &name)
	want, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if name != want {
		t.Fatalf("session_local_hostname = %q, want %q", name, want)
	}

	host := "127.0.0.1"
	port := int32(22)
	username := "root"
	assetRow, err := production.Services.Store.AssetCreate(t.Context(), store.AssetInput{
		Kind: "ssh", Name: "remote", Host: &host, Port: &port, Username: &username,
	})
	if err != nil {
		t.Fatal(err)
	}
	connectedResponse := dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+assetRow.ID+`"}}`, "", "")
	var remote sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &remote)
	response = dispatchDurableTest(t, production, "session_local_hostname", `{"sessionId":"`+remote.ID+`"}`, "", "")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		raw, _ := json.Marshal(response)
		t.Fatalf("remote session hostname response = %s, want unsupported", raw)
	}

	response = dispatchDurableTest(t, production, "session_local_hostname", `{"sessionId":"missing"}`, "", "")
	if response.OK {
		t.Fatal("unknown session returned a hostname")
	}
}
