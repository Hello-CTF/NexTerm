//go:build darwin || linux

package production

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestProductionShutdownDrainsTranscriptEndAndOffset(t *testing.T) {
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	production := newSupervisorTestProduction(t, dataDir, factory, nil)
	connectedResponse := dispatchDurableTest(t, production, "session_connect_local", `null`, "", "")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	channelID := "shutdown-drain-review-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	var tabID string
	requireStoreTestResponse(t, attachResponse, &tabID)
	waitForProductionOutput(t, factory.at(channelID, 0), "$ ")
	writeDurableTestCommand(t, production, tabID, "shutdown-drain-marker", "client-a")
	waitForProductionOutput(t, factory.at(channelID, 0), "shutdown-drain-marker")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := production.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	ctx := context.Background()
	rows, err := reopened.TranscriptListByAsset(ctx, "01J0NEXTERMLOCALDEVICE0001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one transcript after shutdown, got %+v", rows)
	}
	if rows[0].EndedAt == nil {
		t.Fatal("session end must be drained into the transcript before the writer closes")
	}
	if rows[0].Chunks == 0 {
		t.Fatal("recorded output must be flushed before shutdown completes")
	}
	matches, err := reopened.TranscriptSearch(ctx, rows[0].ID, []byte("shutdown-drain-marker"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("recorded output must survive the shutdown drain")
	}
	offset, err := reopened.DurableTranscriptOffsetGet(ctx, tabID)
	if err != nil {
		t.Fatal(err)
	}
	if offset <= 0 {
		t.Fatalf("durable transcribed offset must persist across shutdown, got %d", offset)
	}
}
