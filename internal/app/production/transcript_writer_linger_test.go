package production

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func openLingerTestStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// lingerTranscriptRows polls for the asset's transcripts; the shared session
// helpers are built only on darwin and linux, and this test runs everywhere.
func lingerTranscriptRows(t *testing.T, database *store.Store, ctx context.Context) []store.TranscriptRow {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatal("transcript row was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTranscriptWriterLingerCoalescesChunksAndShutdownDoesNotWait(t *testing.T) {
	ctx := context.Background()
	database := openLingerTestStore(t)
	writer := startTestWriter(t, database, transcriptWriterConfig{BatchLinger: time.Hour})
	startTestRecording(ctx, writer, "session-1", "tab-1")
	for index := 0; index < 20; index++ {
		writer.RecordingOutput(ctx, "session-1", "tab-1", []byte(fmt.Sprintf("chunk-%02d", index)))
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown waited for the linger window: %v", err)
	}
	stats := writer.statsSnapshot()
	if stats.batches != 1 || stats.flushedChunks != 21 {
		t.Fatalf("expected the start resize and the 20 chunks in one append, got %+v", stats)
	}
	rows := lingerTranscriptRows(t, database, ctx)
	if rows[0].Chunks != 21 || rows[0].EndedAt == nil {
		t.Fatalf("transcript not complete after shutdown: %+v", rows[0])
	}
}

func TestTranscriptWriterLingerFlushesFullBatchWithoutWaiting(t *testing.T) {
	ctx := context.Background()
	database := openLingerTestStore(t)
	writer := startTestWriter(t, database, transcriptWriterConfig{BatchLinger: time.Hour})
	startTestRecording(ctx, writer, "session-1", "tab-1")
	for index := 0; index < transcriptBatchMaxItems+10; index++ {
		writer.RecordingOutput(ctx, "session-1", "tab-1", []byte(fmt.Sprintf("chunk-%03d", index)))
	}
	rows := lingerTranscriptRows(t, database, ctx)
	// The start item takes one slot of the first full batch and records the
	// start resize in place of an output chunk.
	waitForTranscriptChunks(t, database, rows[0].ID, transcriptBatchMaxItems)
}
