package store

import "context"

// Test-only exports for the external test package (store_test): they expose
// the timestamp-index internals so four-backend fixtures can drive the
// legacy, pending, and corruption paths without a multi-million-chunk
// dataset.

// TranscriptTSIndexCompleteForTest reports whether the transcript's
// timestamp index is complete.
func (s *Store) TranscriptTSIndexCompleteForTest(ctx context.Context, transcriptID string) (bool, error) {
	return s.transcriptTSIndexComplete(ctx, s.db, transcriptID)
}

// TranscriptSeekScanBudgetForTest returns the scan budget.
func TranscriptSeekScanBudgetForTest() int { return transcriptSeekScanBudget }

// SetTranscriptSeekScanBudgetForTest sets the scan budget.
func SetTranscriptSeekScanBudgetForTest(budget int) { transcriptSeekScanBudget = budget }

// TranscriptBackfillLoopRunningForTest reports whether the store's backfill
// loop is still registered: the loop deletes its registration only as it
// exits, so a false return means every batch has been processed.
func (s *Store) TranscriptBackfillLoopRunningForTest() bool {
	_, running := transcriptTSIndexBackfills.Load(s)
	return running
}
