package durable

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestSessionReadsOldThenNewOutput(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	installRecords(backend, runner, current)
	session := newUnitSession(t, backend, current, []byte("old-"))
	go func() {
		time.Sleep(10 * time.Millisecond)
		file, err := os.OpenFile(backend.recordingPath(current.info.ID), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return
		}
		_, _ = file.Write([]byte("new"))
		_ = file.Close()
	}()
	buffer := make([]byte, len("old-new"))
	if _, err := io.ReadFull(session, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "old-new" {
		t.Fatalf("output = %q", buffer)
	}
}

func TestDetachUnblocksReadWithoutKilling(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	installRecords(backend, runner, current)
	session := newUnitSession(t, backend, current, nil)
	result := make(chan error, 1)
	go func() {
		_, err := session.Read(make([]byte, 1))
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	callsBefore := len(runner.recordedCalls())
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Read error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read remained blocked after Detach")
	}
	for _, call := range runner.recordedCalls()[callsBefore:] {
		if call[0] == "kill-session" {
			t.Fatalf("Detach killed session: %+v", call)
		}
	}
}

func TestDeadSessionCompletionAfterQuietWindow(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	current.info.Dead = true
	current.info.ExitCode = intPointer(7)
	current.deadStatus = "7"
	current.recordingLive = true
	discoveries := 0
	runner.handler = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 0 || args[0] != "list-panes" {
			return nil, nil
		}
		discoveries++
		if discoveries == 3 {
			file, err := os.OpenFile(backend.recordingPath(current.info.ID), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return nil, err
			}
			if _, err := file.Write([]byte("final")); err != nil {
				_ = file.Close()
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, err
			}
			if err := os.WriteFile(backend.recorderDonePath(current.info.ID), []byte("0\n"), 0o600); err != nil {
				return nil, err
			}
		}
		return []byte(discoveryLine(backend, current)), nil
	}
	session := newUnitSession(t, backend, current, []byte("old:"))
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "old:final" {
		t.Fatalf("output = %q, discoveries = %d", output, discoveries)
	}
	if discoveries < 3 {
		t.Fatalf("EOF preceded the delayed completion marker: discoveries = %d", discoveries)
	}
}

func TestWriteEncodesLiteralUnicodeAndControlBytes(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	installRecords(backend, runner, current)
	session := newUnitSession(t, backend, current, nil)
	input := []byte("-hello 世界\x00\x03\r")
	count, err := session.Write(input)
	if err != nil {
		t.Fatal(err)
	}
	if count != len(input) {
		t.Fatalf("Write count = %d, want %d", count, len(input))
	}
	var sends [][]string
	for _, call := range runner.recordedCalls() {
		if call[0] == "send-keys" {
			sends = append(sends, call)
		}
	}
	want := [][]string{
		{"send-keys", "-t", "%1", "-l", "--", "-hello 世界"},
		{"send-keys", "-t", "%1", "-H", "00", "03", "0d"},
	}
	if !reflect.DeepEqual(sends, want) {
		t.Fatalf("send-keys calls:\n got %+v\nwant %+v", sends, want)
	}
}

func TestWriteRejectsInvalidUTF8BeforeSending(t *testing.T) {
	backend, runner := newUnitBackend(t)
	current := unitRecord(backend, ids.New())
	session := newUnitSession(t, backend, current, nil)
	if _, err := session.Write([]byte{'a', 0xff}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Write error = %v, want ErrInvalidInput", err)
	}
	for _, call := range runner.recordedCalls() {
		if call[0] == "send-keys" {
			t.Fatalf("invalid input was partially sent: %+v", call)
		}
	}
}

func TestWriteReportsExitedWithoutSending(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	dead := current
	dead.info.Dead = true
	dead.info.ExitCode = intPointer(1)
	dead.deadStatus = "1"
	dead.recordingLive = false
	installRecords(backend, runner, dead)
	session := newUnitSession(t, backend, current, nil)
	if _, err := session.Write([]byte("x")); !errors.Is(err, ErrExited) {
		t.Fatalf("Write error = %v, want ErrExited", err)
	}
	for _, call := range runner.recordedCalls() {
		if call[0] == "send-keys" {
			t.Fatalf("dead pane received input: %+v", call)
		}
	}
}

func TestSessionKillRejectsReplacementIdentity(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	expected := unitRecord(backend, ids.New())
	replacement := expected
	replacement.info.SessionID = "$2"
	replacement.info.PaneID = "%2"
	replacement.info.PID = 9876
	replacement.paneOption = "%2"
	installRecords(backend, runner, replacement)
	session := newUnitSession(t, backend, expected, []byte("retained"))
	if err := session.Kill(context.Background()); !errors.Is(err, ErrIdentity) {
		t.Fatalf("Kill error = %v, want ErrIdentity", err)
	}
	for _, call := range runner.recordedCalls() {
		if call[0] == "kill-session" {
			t.Fatalf("replacement was killed: %+v", call)
		}
	}
}

func TestResizeTargetsOwnedWindow(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	installRecords(backend, runner, current)
	session := newUnitSession(t, backend, current, nil)
	if err := session.Resize(context.Background(), 132, 43); err != nil {
		t.Fatal(err)
	}
	calls := runner.recordedCalls()
	want := []string{"resize-window", "-t", "@1", "-x", "132", "-y", "43"}
	if !reflect.DeepEqual(calls[len(calls)-1], want) {
		t.Fatalf("resize call = %+v, want %+v", calls[len(calls)-1], want)
	}
}

func TestInputOperationsChunkWithoutSplittingRunes(t *testing.T) {
	input := []byte(strings.Repeat("世", maxLiteralInput))
	operations, err := makeInputOperations(input)
	if err != nil {
		t.Fatal(err)
	}
	var rebuilt []byte
	for _, operation := range operations {
		if !operation.literal || len(operation.data) > maxLiteralInput {
			t.Fatalf("invalid literal operation: %+v", operation)
		}
		rebuilt = append(rebuilt, operation.data...)
	}
	if !bytes.Equal(rebuilt, input) {
		t.Fatal("chunked input differs")
	}
}

func TestConcurrentDetachIsRaceSafe(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	installRecords(backend, runner, current)
	session := newUnitSession(t, backend, current, nil)
	var wait sync.WaitGroup
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = session.Write([]byte("x"))
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		_, _ = session.Read(make([]byte, 1))
	}()
	time.Sleep(10 * time.Millisecond)
	if err := session.Detach(); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
}
