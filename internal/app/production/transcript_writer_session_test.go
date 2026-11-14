//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

func transcriptTestConnector() session.Connector {
	return session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
	})
}

func transcriptOwnerContext(ownerID string) context.Context {
	return ipc.WithUserID(context.Background(), ownerID)
}

func openTranscriptTestTab(t *testing.T, manager *session.Manager, ctx context.Context, sessionID, clientID, channelID string) string {
	t.Helper()
	info, err := manager.OpenTab(ctx, session.OpenTabOptions{
		SessionID: sessionID, ClientID: clientID, ChannelID: channelID, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

func waitForTranscriptRows(t *testing.T, database *store.Store, ctx context.Context, assetID string, count int) []store.TranscriptRow {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var rows []store.TranscriptRow
	for time.Now().Before(deadline) {
		var err error
		rows, err = database.TranscriptListByAsset(ctx, assetID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) >= count {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("transcripts for %s did not reach %d rows: %+v", assetID, count, rows)
	return nil
}

func waitForTranscriptMarker(t *testing.T, database *store.Store, ctx context.Context, transcriptID, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		matches, err := database.TranscriptSearch(ctx, transcriptID, []byte(marker), 0, 0)
		if err == nil && len(matches) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("transcript %s never recorded %q", transcriptID, marker)
}

// Real A/B sessions sharing one asset id drive one writer and one store: every
// Start/Append/End lands under the session's trusted owner, reads stay
// isolated, and a reconnect opens the tab's second recording under the same
// owner.
func TestTranscriptWriterSessionsIsolateOwners(t *testing.T) {
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
	ownerB := transcriptOwnerContext("owner-b")
	asset := session.Asset{ID: "shared-asset", Name: "shared", Kind: session.KindSSH}
	sessionA, err := manager.Connect(ownerA, asset)
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := manager.Connect(ownerB, asset)
	if err != nil {
		t.Fatal(err)
	}
	tabA := openTranscriptTestTab(t, manager, ownerA, sessionA.ID, "client-a", "channel-a")
	tabB := openTranscriptTestTab(t, manager, ownerB, sessionB.ID, "client-b", "channel-b")
	if err := manager.Write(ownerA, tabA, "client-a", []byte("echo owner-a-marker\n")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(ownerB, tabB, "client-b", []byte("echo owner-b-marker\n")); err != nil {
		t.Fatal(err)
	}

	rowsA := waitForTranscriptRows(t, database, ownerA, "shared-asset", 1)
	rowsB := waitForTranscriptRows(t, database, ownerB, "shared-asset", 1)
	if rowsA[0].OwnerID != "owner-a" || rowsB[0].OwnerID != "owner-b" {
		t.Fatalf("transcript owners = %q/%q, want owner-a/owner-b", rowsA[0].OwnerID, rowsB[0].OwnerID)
	}
	if rowsA[0].ID == rowsB[0].ID {
		t.Fatal("owners share a transcript row")
	}
	if _, err := database.TranscriptGet(ownerB, rowsA[0].ID); err == nil {
		t.Fatal("owner-b read owner-a's transcript")
	}
	chunks, err := database.TranscriptChunks(ownerB, rowsA[0].ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("owner-b read owner-a's chunks: %+v", chunks)
	}
	waitForTranscriptMarker(t, database, ownerA, rowsA[0].ID, "owner-a-marker")
	waitForTranscriptMarker(t, database, ownerB, rowsB[0].ID, "owner-b-marker")

	// A reconnect starts the watched tab's second recording, written under
	// the same owner once it records output.
	if err := manager.Disconnect(sessionA.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconnect(ownerA, sessionA.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(ownerA, tabA, "client-a", []byte("echo owner-a-second-marker\n")); err != nil {
		t.Fatal(err)
	}
	rowsA = waitForTranscriptRows(t, database, ownerA, "shared-asset", 2)
	for _, row := range rowsA {
		if row.OwnerID != "owner-a" || row.TabID != tabA {
			t.Fatalf("recording %s = owner %q tab %q, want owner-a and %s", row.ID, row.OwnerID, row.TabID, tabA)
		}
	}
	if rowsA[1].EndedAt == nil {
		t.Fatal("the first recording must be ended before the second begins")
	}
	waitForTranscriptMarker(t, database, ownerA, rowsA[0].ID, "owner-a-second-marker")

	if err := manager.Disconnect(sessionA.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(sessionB.ID); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	rowsA, err = database.TranscriptListByAsset(ownerA, "shared-asset", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rowsA {
		if row.EndedAt == nil {
			t.Fatalf("recording %s must be ended after shutdown: %+v", row.ID, row)
		}
	}
}

func TestTranscriptWriterFailsClosedWithoutOwner(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var logs syncedLogBuffer
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: slog.New(slog.NewTextHandler(&logs, nil)), RequireOwner: true,
	})
	startTestRecording(ctx, writer, "session-ownerless", "tab-1")
	writer.RecordingOutput(ctx, "session-ownerless", "tab-1", []byte("ownerless-output\r\n"))
	writer.RecordingEnded(ctx, "session-ownerless", "tab-1")
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("ownerless writes must fail closed on a non-open server: %+v", rows)
	}
	if !strings.Contains(logs.String(), "failing closed") {
		t.Fatalf("fail-closed warning missing: %s", logs.String())
	}

	// A desktop/open writer keeps the legacy ownerless behavior.
	legacyDatabase, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacyDatabase.Close() })
	legacyWriter := newTranscriptWriter(transcriptWriterConfig{Database: legacyDatabase, Logger: discardTranscriptLogger()})
	startTestRecording(ctx, legacyWriter, "session-legacy", "tab-1")
	legacyWriter.RecordingOutput(ctx, "session-legacy", "tab-1", []byte("legacy-output\r\n"))
	legacyWriter.RecordingEnded(ctx, "session-legacy", "tab-1")
	if err := legacyWriter.Start(ctx); err != nil {
		t.Fatal(err)
	}
	legacyShutdown, legacyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer legacyCancel()
	if err := legacyWriter.Shutdown(legacyShutdown); err != nil {
		t.Fatal(err)
	}
	legacyRows, err := legacyDatabase.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyRows) != 1 || legacyRows[0].OwnerID != "" {
		t.Fatalf("desktop writer must keep the legacy ownerless row: %+v", legacyRows)
	}
}

// Shutdown must stop and join the retention loop before returning, even when
// the caller's context is already exhausted: after Shutdown, no retention run
// may write to the store or the logger again (the M70 post-shutdown write).
func TestTranscriptWriterShutdownJoinsRetentionLoop(t *testing.T) {
	ctx := context.Background()
	var logs syncedLogBuffer
	database, err := store.OpenInMemoryWithOptions(ctx, store.OpenOptions{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: discardTranscriptLogger(), RetentionInterval: 20 * time.Millisecond,
	})
	if err := writer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitForLogCount(t, &logs, "scheduled retention cleanup finished", 1)

	expired, cancel := context.WithCancel(ctx)
	cancel()
	_ = writer.Shutdown(expired)

	frozen := strings.Count(logs.String(), "scheduled retention cleanup finished")
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := strings.Count(logs.String(), "scheduled retention cleanup finished"); got != frozen {
			t.Fatalf("retention wrote after Shutdown returned: count went %d -> %d", frozen, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForLogCount(t *testing.T, logs *syncedLogBuffer, message string, count int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(logs.String(), message) >= count {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("log never reached %d %q entries: %s", count, message, logs.String())
}

// A queue-full drop marker is written under the recording's trusted owner:
// under RequireOwner it neither fails closed nor lands ownerless, also when
// the dropped output is all the recording holds.
func TestTranscriptWriterDropMarkerInheritsOwner(t *testing.T) {
	run := func(t *testing.T, queueMaxBytes int64, outputs int, payload []byte) store.TranscriptRow {
		t.Helper()
		ctx := context.Background()
		database, err := store.OpenInMemory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		writer := newTranscriptWriter(transcriptWriterConfig{
			Database: database, Logger: discardTranscriptLogger(), QueueMaxBytes: queueMaxBytes, RequireOwner: true,
		})
		ownerA := transcriptOwnerContext("owner-a")
		startTestRecording(ownerA, writer, "session-owner-drop", "tab-1")
		for index := 0; index < outputs; index++ {
			writer.RecordingOutput(ownerA, "session-owner-drop", "tab-1", payload)
		}
		writer.RecordingEnded(ownerA, "session-owner-drop", "tab-1")
		if err := writer.Start(ctx); err != nil {
			t.Fatal(err)
		}
		shutdownTestWriter(t, writer, 30*time.Second)
		rows, err := database.TranscriptListByAsset(ownerA, "asset-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected exactly one transcript under owner-a, got %+v", rows)
		}
		row := rows[0]
		if row.OwnerID != "owner-a" || !row.Truncated || row.EndedAt == nil {
			t.Fatalf("transcript state = %+v, want owner-a, truncated, ended", row)
		}
		chunks, err := database.TranscriptChunks(ownerA, row.ID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunks) == 0 || !bytes.Contains(chunks[len(chunks)-1].Data, []byte("段输出")) {
			t.Fatalf("the omission marker must be written under the owner: %+v", chunks)
		}
		return row
	}

	t.Run("marker after accepted output", func(t *testing.T) {
		row := run(t, 4096, 64, bytes.Repeat([]byte("y"), 1024))
		if row.Chunks != 6 {
			t.Fatalf("expected the start resize, 4 accepted chunks and the marker: %+v", row)
		}
	})
	t.Run("marker is all the output", func(t *testing.T) {
		row := run(t, 1, 3, []byte("dropped"))
		if row.Chunks != 2 {
			t.Fatalf("expected the start resize and the marker: %+v", row)
		}
	})
}
