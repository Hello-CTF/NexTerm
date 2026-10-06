//go:build darwin || linux

package production

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestProductionShutdownDrainsTranscriptEndAndOffset(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	dataDir := durableTestDataDir(t)
	factory := &bridgeTestFactory{}
	database, err := store.Open(t.Context(), filepath.Join(dataDir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	transcripts := newTranscriptWriter(transcriptWriterConfig{Database: database, Logger: logger})
	manager := session.NewManager(session.Config{
		Connector:         fakeSSHConnector{},
		DurableResolver:   resolver,
		TranscriptOffsets: durableTranscriptOffsets{database: database},
		Transcripts:       transcripts,
	})
	production, err := NewProductionWithServices(Config{
		Logger:  logger,
		Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
	}, ProductionServices{Store: database, Sessions: manager, Transcripts: transcripts})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	connected := connectRemoteSSHAsset(t, production)
	tabID := createDaemonTab(t, fixture.provider, "shutdown-drain-marker")
	channelID := "shutdown-drain-review-channel"
	recoveredResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, channelID, "client-a")
	var recovered attachedTabDTO
	requireStoreTestResponse(t, recoveredResponse, &recovered)
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
	rows, err := reopened.TranscriptListByAsset(ctx, *connected.AssetID, 0)
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
