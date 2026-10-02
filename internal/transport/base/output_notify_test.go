package base

import (
	"context"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"
)

func TestOutputRouterWrongWaiterCannotStealWriterWakeup(t *testing.T) {
	processors := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(processors)
	router := NewOutputRouter(context.Background())
	stdout := router.Writer(false)
	stderr := router.Writer(true)
	_, _ = stdout.Write([]byte("p"))
	primer := make([]byte, 1)
	if count, err := router.ReadStdout(primer); err != nil || count != 1 {
		t.Fatalf("select raw mode = %d, %v", count, err)
	}
	for range outputQueueEvents {
		_, _ = stdout.Write([]byte("o"))
		_, _ = stderr.Write([]byte("e"))
	}
	wrong := make(chan error, 1)
	eligible := make(chan error, 1)
	go func() { wrong <- writeAll(stderr, []byte("wrong")) }()
	go func() { eligible <- writeAll(stdout, []byte("eligible")) }()
	runtime.Gosched()
	wakeup := outputWakeup(router)
	buffer := make([]byte, 1)
	if count, err := router.ReadStdout(buffer); err != nil || count != 1 {
		t.Fatalf("read to free stdout capacity = %d, %v", count, err)
	}
	<-wakeup
	select {
	case err := <-eligible:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("eligible stdout writer slept after a wrong waiter consumed the notification")
	}
	router.Close()
	select {
	case err := <-wrong:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("wrong writer error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake the remaining writer")
	}
}

func TestOutputRouterWrongWaiterCannotStealReaderWakeup(t *testing.T) {
	processors := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(processors)
	router := NewOutputRouter(context.Background())
	type readResult struct {
		data string
		err  error
	}
	stdoutResult := make(chan readResult, 1)
	stderrResult := make(chan readResult, 1)
	go func() {
		buffer := make([]byte, 1)
		count, err := router.ReadStdout(buffer)
		stdoutResult <- readResult{data: string(buffer[:count]), err: err}
	}()
	go func() {
		buffer := make([]byte, 1)
		count, err := router.Stderr().Read(buffer)
		stderrResult <- readResult{data: string(buffer[:count]), err: err}
	}()
	runtime.Gosched()
	wakeup := outputWakeup(router)
	_, _ = router.Writer(false).Write([]byte("x"))
	<-wakeup
	select {
	case result := <-stdoutResult:
		if result.err != nil || result.data != "x" {
			t.Fatalf("stdout read = %q, %v", result.data, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("eligible stdout reader slept after a wrong waiter consumed the notification")
	}
	router.Close()
	select {
	case result := <-stderrResult:
		if !errors.Is(result.err, io.EOF) {
			t.Fatalf("stderr close result = %q, %v", result.data, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake the remaining reader")
	}
}

func outputWakeup(router *OutputRouter) chan struct{} {
	router.mu.Lock()
	defer router.mu.Unlock()
	return router.notify
}
