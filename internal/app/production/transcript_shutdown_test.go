//go:build darwin || linux

package production

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestProductionShutdownDrainsTranscriptEnd(t *testing.T) {
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
		Connector:       fakeSSHConnector{},
		DurableResolver: resolver,
		Transcripts:     transcripts,
		OpenAccess:      true,
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
	// Only output produced while a client watches is recorded, so the marker
	// is printed after the attach, in answer to input.
	tabID := ids.New()
	daemon, err := fixture.provider.Create(t.Context(), base.DurableCreateOptions{
		ID: tabID, Command: []string{"sh", "-c", "printf 'ready\\n'; read line; printf '%s-drained\\n' \"$line\"; sleep 30"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	readUntilDurableOutput(t, daemon, "ready")
	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}
	channelID := "shutdown-drain-review-channel"
	recoveredResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, channelID, "client-a")
	var recovered attachedTabDTO
	requireStoreTestResponse(t, recoveredResponse, &recovered)
	waitForProductionOutput(t, factory.at(channelID, 0), "ready")
	if err := production.Services.Sessions.Write(context.Background(), tabID, "client-a", []byte("shutdown\n")); err != nil {
		t.Fatal(err)
	}
	waitForProductionOutput(t, factory.at(channelID, 0), "shutdown-drained")

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
		t.Fatal("the recording's end must be drained into the transcript before the writer closes")
	}
	if rows[0].Chunks == 0 {
		t.Fatal("recorded output must be flushed before shutdown completes")
	}
	matches, err := reopened.TranscriptSearch(ctx, rows[0].ID, []byte("shutdown-drained"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("recorded output must survive the shutdown drain")
	}
}
