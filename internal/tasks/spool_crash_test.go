package tasks

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

// crashAt arms fault injection and runs one Write that must panic at the
// requested compaction stage, leaving the on-disk state mid-transaction.
func crashAt(t *testing.T, s *spool, stage string, p []byte) {
	t.Helper()
	s.crashHook = func(got string) {
		if got == stage {
			panic("simulated crash at " + stage)
		}
	}
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		_, _ = s.Write(p)
	}()
	s.crashHook = nil
	if !panicked {
		t.Fatalf("compaction never reached stage %q", stage)
	}
}

// assertSpoolState verifies retained content, Total/Dropped, and the logical
// offsets of incremental reads, including the Gap jump over the elided middle.
func assertSpoolState(t *testing.T, s *spool, wantTotal, wantDropped int64, wantHead, wantTail []byte) {
	t.Helper()
	out, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != wantTotal || out.Dropped != wantDropped ||
		!bytes.Equal(out.Head, wantHead) || !bytes.Equal(out.Tail, wantTail) {
		t.Fatalf("state mismatch: total=%d/%d dropped=%d/%d head=%q/%q tail=%q/%q",
			out.Total, wantTotal, out.Dropped, wantDropped, out.Head, wantHead, out.Tail, wantTail)
	}
	tailStart := wantTotal - int64(len(wantTail))
	chunk, err := s.Read(tailStart, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Offset != tailStart || !bytes.Equal(chunk.Data, wantTail) || chunk.Next != wantTotal || chunk.Total != wantTotal {
		t.Fatalf("tail exposed at wrong logical offset: %+v", chunk)
	}
	headChunk, err := s.Read(0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(headChunk.Data, wantHead) || headChunk.Next != int64(len(wantHead)) {
		t.Fatalf("head read mismatch: %+v", headChunk)
	}
	if wantDropped > 0 {
		gap, err := s.Read(int64(len(wantHead)), 1024)
		if err != nil {
			t.Fatal(err)
		}
		if !gap.Gap || gap.Offset != tailStart || !bytes.Equal(gap.Data, wantTail) {
			t.Fatalf("gap read mismatch: %+v", gap)
		}
	}
}

// TestSpoolCrashBeforeJournal crashes after the prepared tail is written but
// before the journal commits. Recovery must restore the exact pre-compaction
// state: nothing was elided yet.
func TestSpoolCrashBeforeJournal(t *testing.T) {
	dir := t.TempDir()
	s, err := createSpool(dir, "c1", 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	crashAt(t, s, "tmp", sequence(40))
	reopened, err := openSpool(dir, "c1", 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	assertSpoolState(t, reopened, 40, 0, sequence(40)[:4], sequence(40)[4:])
	if _, err := os.Stat(reopened.tailPath() + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared tail not cleaned: %v", err)
	}
}

// TestSpoolCrashCompactionWindows covers the review's 4/8-limit 40-byte case
// with a zero starting index: after the journal commits, after the tail
// rename, and after the index commit. Every window must reopen with exact
// Total=40, Dropped=28, and identical logical offsets.
func TestSpoolCrashCompactionWindows(t *testing.T) {
	for _, stage := range []string{"journal", "rename", "index"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			s, err := createSpool(dir, "c2", 4, 8)
			if err != nil {
				t.Fatal(err)
			}
			crashAt(t, s, stage, sequence(40))
			reopened, err := openSpool(dir, "c2", 4, 8)
			if err != nil {
				t.Fatal(err)
			}
			assertSpoolState(t, reopened, 40, 28, sequence(40)[:4], sequence(40)[32:])
			if _, err := os.Stat(reopened.compactPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal not resolved: %v", err)
			}
			if _, err := os.Stat(reopened.tailPath() + ".tmp"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("prepared tail not cleaned: %v", err)
			}
			// Repair must be idempotent: a second open sees the same state.
			again, err := openSpool(dir, "c2", 4, 8)
			if err != nil {
				t.Fatal(err)
			}
			assertSpoolState(t, again, 40, 28, sequence(40)[:4], sequence(40)[32:])
		})
	}
}

// TestSpoolCrashWithPriorCheckpoint repeats the crash windows with a prior
// non-zero checkpoint: a first completed compaction at 40 bytes, then a
// second compaction at 64 bytes. The stale index previously produced false
// totals whenever the new tail matched the checkpointed size again.
func TestSpoolCrashWithPriorCheckpoint(t *testing.T) {
	for _, stage := range []string{"journal", "rename", "index"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			s, err := createSpool(dir, "c3", 4, 8)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Write(sequence(40)); err != nil {
				t.Fatal(err)
			}
			crashAt(t, s, stage, sequence(64)[40:])
			reopened, err := openSpool(dir, "c3", 4, 8)
			if err != nil {
				t.Fatal(err)
			}
			assertSpoolState(t, reopened, 64, 52, sequence(64)[:4], sequence(64)[56:])
			again, err := openSpool(dir, "c3", 4, 8)
			if err != nil {
				t.Fatal(err)
			}
			assertSpoolState(t, again, 64, 52, sequence(64)[:4], sequence(64)[56:])
		})
	}
}
