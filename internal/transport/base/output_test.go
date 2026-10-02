package base

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestOutputRouterSequencesNativeWrites(t *testing.T) {
	router := NewOutputRouter(context.Background())
	stdout := router.Writer(false)
	stderr := router.Writer(true)
	_, _ = stdout.Write([]byte("out-1"))
	_, _ = stderr.Write([]byte("err-1"))
	_, _ = stdout.Write([]byte("out-2"))
	_, _ = stderr.Write([]byte("err-2"))
	expected := []struct {
		data   string
		stderr bool
	}{
		{data: "out-1"},
		{data: "err-1", stderr: true},
		{data: "out-2"},
		{data: "err-2", stderr: true},
	}
	for index, want := range expected {
		event, err := router.NextOutput(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if string(event.Data) != want.data || event.Stderr != want.stderr || event.Sequence != uint64(index+1) {
			t.Fatalf("event %d = %+v", index, event)
		}
	}
	if _, err := router.ReadStdout(make([]byte, 1)); !errors.Is(err, ErrOutputMode) {
		t.Fatalf("raw read after ordered selection = %v", err)
	}
	router.Close()
	if _, err := router.NextOutput(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("next after close = %v", err)
	}
}

func TestOutputRouterRawStreamsStaySeparated(t *testing.T) {
	router := NewOutputRouter(context.Background())
	_, _ = router.Writer(false).Write([]byte("stdout"))
	_, _ = router.Writer(true).Write([]byte("stderr"))
	stdout := make([]byte, 6)
	if count, err := router.ReadStdout(stdout); err != nil || count != 6 || string(stdout) != "stdout" {
		t.Fatalf("raw stdout = %q, %d, %v", stdout, count, err)
	}
	stderr := make([]byte, 6)
	if count, err := router.Stderr().Read(stderr); err != nil || count != 6 || string(stderr) != "stderr" {
		t.Fatalf("raw stderr = %q, %d, %v", stderr, count, err)
	}
	if _, err := router.NextOutput(t.Context()); !errors.Is(err, ErrOutputMode) {
		t.Fatalf("ordered read after raw selection = %v", err)
	}
	router.Close()
}

func TestOutputRouterBackpressureAndClose(t *testing.T) {
	router := NewOutputRouter(context.Background())
	writer := router.Writer(false)
	for range outputQueueEvents {
		if _, err := writer.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	blocked := make(chan error, 1)
	go func() { blocked <- writeAll(writer, []byte("released")) }()
	select {
	case err := <-blocked:
		t.Fatalf("write did not apply backpressure: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	event, err := router.NextOutput(t.Context())
	if err != nil || event.Sequence != 1 {
		t.Fatalf("first event = %+v, %v", event, err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not release blocked writer")
	}
	for sequence := 2; sequence <= outputQueueEvents+1; sequence++ {
		event, err := router.NextOutput(t.Context())
		if err != nil || event.Sequence != uint64(sequence) {
			t.Fatalf("event %d = %+v, %v", sequence, event, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := router.NextOutput(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled NextOutput = %v", err)
	}
	router.Close()
	if _, err := router.NextOutput(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("next after close = %v", err)
	}
}

func TestOutputRouterCloseUnblocksWriter(t *testing.T) {
	router := NewOutputRouter(context.Background())
	writer := router.Writer(false)
	for range outputQueueEvents {
		_, _ = writer.Write([]byte("x"))
	}
	blocked := make(chan error, 1)
	go func() { blocked <- writeAll(writer, []byte("blocked")) }()
	router.Close()
	select {
	case err := <-blocked:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked writer error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock writer")
	}
}

func writeAll(writer io.Writer, data []byte) error {
	_, err := writer.Write(data)
	return err
}
