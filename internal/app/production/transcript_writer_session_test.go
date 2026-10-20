//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
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

// syncedLogBuffer is a race-safe log sink: tests poll it while loggers write
// concurrently, which a plain bytes.Buffer would race on.
type syncedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncedLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *syncedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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

func waitForDurableOffset(t *testing.T, database *store.Store, ctx context.Context, durableID string, want int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		offset, err := database.DurableTranscriptOffsetGet(ctx, durableID)
		if err != nil {
			t.Fatal(err)
		}
		if offset == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	offset, _ := database.DurableTranscriptOffsetGet(ctx, durableID)
	t.Fatalf("durable offset for %s never reached %d, got %d", durableID, want, offset)
}

// Real A/B sessions sharing one asset id drive one writer and one store: every
// Start/Append/End lands under the session's trusted owner, reads and durable
// offsets stay isolated, and a reconnect opens the second period under the
// same owner.
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

	// Durable offsets advance under the same trusted owner that wrote the period.
	writer.SessionOutput(ownerA, "shared-durable-tab", sessionA.ID, tabA, []byte("durable-bytes"))
	waitForDurableOffset(t, database, ownerA, "shared-durable-tab", int64(len("durable-bytes")))
	if offset, err := database.DurableTranscriptOffsetGet(ownerB, "shared-durable-tab"); err != nil || offset != 0 {
		t.Fatalf("owner-b durable offset = %d err=%v, want no cross-owner read", offset, err)
	}

	// Reconnect opens the second period; it must write under the same owner.
	if err := manager.Disconnect(sessionA.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconnect(ownerA, sessionA.ID); err != nil {
		t.Fatal(err)
	}
	rowsA = waitForTranscriptRows(t, database, ownerA, "shared-asset", 2)
	for _, row := range rowsA {
		if row.OwnerID != "owner-a" {
			t.Fatalf("period %s owner = %q, want owner-a", row.ID, row.OwnerID)
		}
	}
	if rowsA[1].EndedAt == nil {
		t.Fatal("the first period must be ended before the second begins")
	}
	if err := manager.Write(ownerA, tabA, "client-a", []byte("echo owner-a-second-marker\n")); err != nil {
		t.Fatal(err)
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
			t.Fatalf("period %s must be ended after shutdown: %+v", row.ID, row)
		}
	}
}

// The production adapters isolate durable offsets and tab titles per owner on
// a real store: the first owner claims the shared row, every other owner's
// reads return the zero value and their writes and deletes are skipped, and a
// legacy ownerless row is claimable exactly once.
func TestDurableTranscriptAdaptersIsolateOwners(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	offsets := durableTranscriptOffsets{database: database}
	titles := durableTabTitles{database: database}
	ownerA := transcriptOwnerContext("owner-a")
	ownerB := transcriptOwnerContext("owner-b")
	const tabID = "shared-tab"

	titles.PersistDurableTabTitleContext(ownerA, tabID, "title-a")
	if got := titles.DurableTabTitleContext(ownerB, tabID); got != "" {
		t.Fatalf("owner-b read owner-a's title %q", got)
	}
	titles.PersistDurableTabTitleContext(ownerB, tabID, "title-b")
	if got := titles.DurableTabTitleContext(ownerA, tabID); got != "title-a" {
		t.Fatalf("owner-b's write touched owner-a's title: %q", got)
	}
	if got := titles.DurableTabTitleContext(ownerB, tabID); got != "" {
		t.Fatalf("owner-b read a row claimed by owner-a: %q", got)
	}
	titles.DeleteDurableTabTitleContext(ownerB, tabID)
	if got := titles.DurableTabTitleContext(ownerA, tabID); got != "title-a" {
		t.Fatalf("owner-b's delete removed owner-a's title: %q", got)
	}

	offsets.PersistDurableTranscriptOffsetContext(ownerA, tabID, 100)
	if got := offsets.DurableTranscriptCatchUpBytesContext(ownerB, tabID); got != 0 {
		t.Fatalf("owner-b catch-up = %d, want 0 on a shared tab id", got)
	}
	offsets.PersistDurableTranscriptOffsetContext(ownerB, tabID, 7)
	if got := offsets.DurableTranscriptCatchUpBytesContext(ownerA, tabID); got != 100 {
		t.Fatalf("owner-b's write touched owner-a's offset: %d", got)
	}
	offsets.DeleteDurableTranscriptOffsetContext(ownerB, tabID)
	if got := offsets.DurableTranscriptCatchUpBytesContext(ownerA, tabID); got != 100 {
		t.Fatalf("owner-b's delete removed owner-a's offset: %d", got)
	}

	// Legacy ownerless rows stay claimable by the first owner that writes them,
	// and a claimed row rejects every other owner.
	if err := database.DurableTabTitleSet(context.Background(), "legacy-tab", "legacy-title"); err != nil {
		t.Fatal(err)
	}
	titles.PersistDurableTabTitleContext(ownerA, "legacy-tab", "claimed")
	if got := titles.DurableTabTitleContext(ownerA, "legacy-tab"); got != "claimed" {
		t.Fatalf("owner-a claim on a legacy ownerless row = %q, want claimed", got)
	}
	titles.PersistDurableTabTitleContext(ownerB, "legacy-tab", "hijack")
	if got := titles.DurableTabTitleContext(ownerA, "legacy-tab"); got != "claimed" {
		t.Fatalf("owner-b overwrote a claimed row: %q", got)
	}
	if got := titles.DurableTabTitleContext(ownerB, "legacy-tab"); got != "" {
		t.Fatalf("owner-b read a row claimed by owner-a: %q", got)
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
	writer.SessionStarted(ctx, transcriptIdentity("session-ownerless"))
	writer.SessionOutput(ctx, "", "session-ownerless", "tab-1", []byte("ownerless-output\r\n"))
	writer.SessionEnded(ctx, "session-ownerless")
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
	legacyWriter.SessionStarted(ctx, transcriptIdentity("session-legacy"))
	legacyWriter.SessionOutput(ctx, "", "session-legacy", "tab-1", []byte("legacy-output\r\n"))
	legacyWriter.SessionEnded(ctx, "session-legacy")
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
