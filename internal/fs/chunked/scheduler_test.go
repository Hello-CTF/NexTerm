package chunked_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func stageGroup(t *testing.T, dir, uploadID string) string {
	t.Helper()
	remote := filepath.Join(dir, "remote.bin")
	destination := fslocal.New()
	upload, err := chunked.New(uploadID, remote, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, upload, destination, 0, "abcd")
	return remote
}

func ageGroup(t *testing.T, dir, uploadID string) {
	t.Helper()
	old := time.Now().Add(-2 * time.Hour)
	for _, match := range stagingGlob(t, filepath.Join(dir, "remote.bin"), uploadID) {
		if err := os.Chtimes(match, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchedulerSweepReapsStaleGroupsAcrossTargets(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	destination := fslocal.New()
	stageGroup(t, dirA, "00000000000000000000000001")
	stageGroup(t, dirB, "00000000000000000000000002")
	fresh := t.TempDir()
	stageGroup(t, fresh, "00000000000000000000000003")
	ageGroup(t, dirA, "00000000000000000000000001")
	ageGroup(t, dirB, "00000000000000000000000002")

	scheduler := chunked.NewScheduler(time.Hour, time.Hour, func(context.Context) ([]chunked.Target, error) {
		return []chunked.Target{
			{FileSystem: destination, Dir: dirA},
			{FileSystem: destination, Dir: dirB},
			{FileSystem: destination, Dir: fresh},
		}, nil
	}, nil)

	removed, err := scheduler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed == 0 {
		t.Fatal("Sweep removed nothing")
	}
	if matches := stagingGlob(t, filepath.Join(dirA, "remote.bin"), "00000000000000000000000001"); len(matches) != 0 {
		t.Fatalf("stale group A survived: %v", matches)
	}
	if matches := stagingGlob(t, filepath.Join(dirB, "remote.bin"), "00000000000000000000000002"); len(matches) != 0 {
		t.Fatalf("stale group B survived: %v", matches)
	}
	if matches := stagingGlob(t, filepath.Join(fresh, "remote.bin"), "00000000000000000000000003"); len(matches) == 0 {
		t.Fatal("fresh group was reaped")
	}
}

func TestSchedulerSweepReportsTargetErrorsAndContinues(t *testing.T) {
	dir := t.TempDir()
	destination := fslocal.New()
	stageGroup(t, dir, "00000000000000000000000001")
	ageGroup(t, dir, "00000000000000000000000001")

	targetErr := errors.New("target listing failed")
	var reported []error
	scheduler := chunked.NewScheduler(time.Hour, time.Hour, func(context.Context) ([]chunked.Target, error) {
		return []chunked.Target{
			{FileSystem: failingListFS{FileSystem: destination, err: targetErr}, Dir: dir},
			{FileSystem: destination, Dir: dir},
		}, nil
	}, func(err error) { reported = append(reported, err) })

	removed, err := scheduler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed == 0 {
		t.Fatal("healthy target was not reaped")
	}
	if len(reported) != 1 || !errors.Is(reported[0], targetErr) {
		t.Fatalf("reported errors = %v", reported)
	}
}

func TestSchedulerSweepPropagatesTargetEnumerationError(t *testing.T) {
	enumerateErr := errors.New("enumeration failed")
	scheduler := chunked.NewScheduler(time.Hour, time.Hour, func(context.Context) ([]chunked.Target, error) {
		return nil, enumerateErr
	}, nil)
	if _, err := scheduler.Sweep(context.Background()); !errors.Is(err, enumerateErr) {
		t.Fatalf("Sweep error = %v, want %v", err, enumerateErr)
	}
}

func TestSchedulerSweepStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	destination := fslocal.New()
	stageGroup(t, dir, "00000000000000000000000001")
	ageGroup(t, dir, "00000000000000000000000001")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler := chunked.NewScheduler(time.Hour, time.Hour, func(context.Context) ([]chunked.Target, error) {
		return []chunked.Target{{FileSystem: destination, Dir: dir}}, nil
	}, nil)
	if _, err := scheduler.Sweep(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sweep error = %v, want context.Canceled", err)
	}
	if matches := stagingGlob(t, filepath.Join(dir, "remote.bin"), "00000000000000000000000001"); len(matches) == 0 {
		t.Fatal("canceled sweep reaped staging")
	}
}

func TestSchedulerRunSweepsOnIntervalUntilCanceled(t *testing.T) {
	dir := t.TempDir()
	destination := fslocal.New()
	stageGroup(t, dir, "00000000000000000000000001")
	ageGroup(t, dir, "00000000000000000000000001")
	var sweeps int
	scheduler := chunked.NewScheduler(5*time.Millisecond, 0, func(context.Context) ([]chunked.Target, error) {
		sweeps++
		return []chunked.Target{{FileSystem: destination, Dir: dir}}, nil
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Run(ctx)
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
	if sweeps < 2 {
		t.Fatalf("sweeps = %d, want at least 2", sweeps)
	}
	if matches := stagingGlob(t, filepath.Join(dir, "remote.bin"), "00000000000000000000000001"); len(matches) != 0 {
		t.Fatalf("stale group survived scheduled sweeps: %v", matches)
	}
}

type failingListFS struct {
	*fslocal.FileSystem
	err error
}

func (f failingListFS) List(context.Context, string) ([]base.FileEntry, error) {
	return nil, f.err
}
