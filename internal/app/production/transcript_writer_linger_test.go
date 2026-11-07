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

func TestTranscriptWriterLingerCoalescesChunksAndShutdownDoesNotWait(t *testing.T) {
	ctx := context.Background()
	database := openLingerTestStore(t)
	writer := startTestWriter(t, database, transcriptWriterConfig{BatchLinger: time.Hour})
	writer.SessionStarted(ctx, transcriptIdentity("session-1"))
	for index := 0; index < 20; index++ {
		writer.SessionOutput(ctx, "", "session-1", "tab-1", []byte(fmt.Sprintf("chunk-%02d", index)))
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown waited for the linger window: %v", err)
	}
	stats := writer.statsSnapshot()
	if stats.batches != 1 || stats.flushedChunks != 20 {
		t.Fatalf("expected the 20 chunks in one append, got %+v", stats)
	}
	rows := waitForTranscriptRows(t, database, ctx, "asset-1", 1)
	if rows[0].Chunks != 20 || rows[0].EndedAt == nil {
		t.Fatalf("transcript not complete after shutdown: %+v", rows[0])
	}
}

func TestTranscriptWriterLingerFlushesFullBatchWithoutWaiting(t *testing.T) {
	ctx := context.Background()
	database := openLingerTestStore(t)
	writer := startTestWriter(t, database, transcriptWriterConfig{BatchLinger: time.Hour})
	writer.SessionStarted(ctx, transcriptIdentity("session-1"))
	for index := 0; index < transcriptBatchMaxItems+10; index++ {
		writer.SessionOutput(ctx, "", "session-1", "tab-1", []byte(fmt.Sprintf("chunk-%03d", index)))
	}
	rows := waitForTranscriptRows(t, database, ctx, "asset-1", 1)
	// The start item takes one slot of the first full batch.
	waitForTranscriptChunks(t, database, rows[0].ID, transcriptBatchMaxItems-1)
}
