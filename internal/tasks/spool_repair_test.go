package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

var errIndexFault = errors.New("injected index repair failure")

func faultIndexWrites(t *testing.T) (restore func()) {
	t.Helper()
	restore = spoolIndexWriter.swap(func(path string, value any) error { return errIndexFault })
	t.Cleanup(restore)
	return restore
}

func runRepairFaultOpens(t *testing.T, dir, base string, failCount int, wantTotal, wantDropped int64, wantHead, wantTail []byte) {
	t.Helper()
	restore := faultIndexWrites(t)
	for i := 0; i < failCount; i++ {
		rec, err := openSpool(dir, base, 4, 8)
		if !errors.Is(err, errIndexFault) {
			t.Fatalf("open %d: expected injected repair failure, got %v", i, err)
		}
		assertSpoolState(t, rec, wantTotal, wantDropped, wantHead, wantTail)
		if _, statErr := os.Stat(rec.compactPath()); statErr != nil {
			t.Fatalf("open %d: journal deleted despite failed repair: %v", i, statErr)
		}
	}
	restore()
	fixed, err := openSpool(dir, base, 4, 8)
	if err != nil {
		t.Fatalf("retry after restoring writes: %v", err)
	}
	assertSpoolState(t, fixed, wantTotal, wantDropped, wantHead, wantTail)
	if _, statErr := os.Stat(fixed.compactPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("journal not cleaned after durable repair: %v", statErr)
	}
	if _, statErr := os.Stat(fixed.tailPath() + ".tmp"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("prepared tail not cleaned after durable repair: %v", statErr)
	}
	again, err := openSpool(dir, base, 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	assertSpoolState(t, again, wantTotal, wantDropped, wantHead, wantTail)
}

func TestFaultIndexWritesConcurrentSwap(t *testing.T) {
	dir := t.TempDir()
	s, err := createSpool(dir, "swap", 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	const iterations = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			s.mu.Lock()
			_ = s.writeIndexLocked()
			s.mu.Unlock()
		}
	}()
	for i := 0; i < iterations; i++ {
		restore := spoolIndexWriter.swap(func(path string, value any) error { return errIndexFault })
		restore()
	}
	<-done
}

func TestSpoolRepairFailureRepeatedOpens(t *testing.T) {
	for _, stage := range []string{"journal", "rename"} {
		for _, failCount := range []int{1, 3} {
			t.Run(fmt.Sprintf("%s-40-fail%d", stage, failCount), func(t *testing.T) {
				dir := t.TempDir()
				s, err := createSpool(dir, "r", 4, 8)
				if err != nil {
					t.Fatal(err)
				}
				crashAt(t, s, stage, sequence(40))
				runRepairFaultOpens(t, dir, "r", failCount, 40, 28, sequence(40)[:4], sequence(40)[32:])
			})
			t.Run(fmt.Sprintf("%s-64-fail%d", stage, failCount), func(t *testing.T) {
				dir := t.TempDir()
				s, err := createSpool(dir, "r", 4, 8)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.Write(sequence(40)); err != nil {
					t.Fatal(err)
				}
				crashAt(t, s, stage, sequence(64)[40:])
				runRepairFaultOpens(t, dir, "r", failCount, 64, 52, sequence(64)[:4], sequence(64)[56:])
			})
		}
	}
}

func TestOpenSurfacesRepairFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := createSpool(dir, "r5", 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	crashAt(t, s, "rename", sequence(40))
	info := Info{
		ID:        "r5",
		Owner:     ownerA,
		Command:   Command{Path: "/bin/sh"},
		State:     StateRunning,
		Detached:  true,
		CreatedAt: time.Now(),
	}
	if err := writeMetaFile(dir, info); err != nil {
		t.Fatal(err)
	}
	restore := faultIndexWrites(t)
	if _, err := Open(Options{Dir: dir}); !errors.Is(err, errIndexFault) {
		t.Fatalf("Open must surface the repair failure, got %v", err)
	}
	if onDisk := readMeta(t, dir, "r5"); onDisk.State != StateRunning {
		t.Fatalf("record changed despite failed open: %+v", onDisk)
	}
	restore()
	m := reopenManager(t, dir)
	got, err := m.Get(context.Background(), ownerA, "r5")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateInterrupted || got.OutputBytes != 40 || got.DroppedBytes != 28 {
		t.Fatalf("unexpected recovered record: %+v", got)
	}
}
