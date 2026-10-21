//go:build darwin || linux

package production

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// A reconnect must hand the tab a replacement channel that outlives the
// reconnect call itself: the transports bind an opened channel's lifetime to
// the context passed to OpenPTY, so opening with the attempt-scoped context
// killed the fresh shell moments after Reconnect returned — the next Write
// failed with "terminal tab closed" or its output never reached the
// transcript. Waiting past that window makes the interleaving deterministic:
// the replacement channel must still carry output into the second period
// under the same owner.
func TestTranscriptWriterReconnectKeepsReplacementChannelAlive(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), RequireOwner: true,
	})
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = writer.Shutdown(shutdownCtx)
	})
	manager := session.NewManager(session.Config{
		Connector:        transcriptTestConnector(),
		Transcripts:      writer,
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })

	ownerA := transcriptOwnerContext("owner-a")
	asset := session.Asset{ID: "shared-asset", Name: "shared", Kind: session.KindSSH}
	sessionA, err := manager.Connect(ownerA, asset)
	if err != nil {
		t.Fatal(err)
	}
	tabA := openTranscriptTestTab(t, manager, ownerA, sessionA.ID, "client-a", "channel-a")
	if err := manager.Write(ownerA, tabA, "client-a", []byte("echo owner-a-marker\n")); err != nil {
		t.Fatal(err)
	}
	rowsA := waitForTranscriptRows(t, database, ownerA, "shared-asset", 1)
	waitForTranscriptMarker(t, database, ownerA, rowsA[0].ID, "owner-a-marker")

	if err := manager.Disconnect(sessionA.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconnect(ownerA, sessionA.ID); err != nil {
		t.Fatal(err)
	}
	rowsA = waitForTranscriptRows(t, database, ownerA, "shared-asset", 2)

	// The replacement channel must survive past the reconnect call: when it is
	// bound to the reconnect attempt's context it is closed asynchronously
	// moments after Reconnect returns, so a write after this window used to
	// fail with "terminal tab closed" or vanish from the transcript.
	time.Sleep(500 * time.Millisecond)
	if err := manager.Write(ownerA, tabA, "client-a", []byte("echo owner-a-second-marker\n")); err != nil {
		t.Fatalf("write on the reconnected tab: %v", err)
	}
	waitForTranscriptMarker(t, database, ownerA, rowsA[0].ID, "owner-a-second-marker")
}
