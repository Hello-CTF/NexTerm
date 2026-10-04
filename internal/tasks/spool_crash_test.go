package tasks

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

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
			again, err := openSpool(dir, "c2", 4, 8)
			if err != nil {
				t.Fatal(err)
			}
			assertSpoolState(t, again, 40, 28, sequence(40)[:4], sequence(40)[32:])
		})
	}
}

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
