//go:build darwin || linux

package production

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestProductionSessionLivenessProbe(t *testing.T) {
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
	response := dispatchDurableTest(t, production, "session_liveness_probe", `{"sessionId":"`+local.ID+`"}`, "", "")
	var probed sessionInfoDTO
	requireStoreTestResponse(t, response, &probed)
	if probed.ID != local.ID || probed.Status != "connected" || probed.Liveness != "" {
		t.Fatalf("session_liveness_probe = %+v, want the connected session without a liveness warning", probed)
	}

	response = dispatchDurableTest(t, production, "session_liveness_probe", `{"sessionId":"missing"}`, "", "")
	if response.OK || response.Error == nil {
		t.Fatalf("probe of an unknown session = %+v, want an error", response)
	}
}
