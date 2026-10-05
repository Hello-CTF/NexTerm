package session

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloseTabStopsActiveRecordingAndClosesFile(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "recording-close", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	before := descriptorCount(t)
	path := filepath.Join(t.TempDir(), "active.log")
	if err := manager.StartRecording(tab.ID, path); err != nil {
		t.Fatal(err)
	}
	if during := descriptorCount(t); during != before+1 {
		t.Fatalf("recording opened %d descriptors, want one", during-before)
	}
	if err := connector.transport(0).channel(0).emit([]byte("active-record")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		count, err := manager.RecordingBytes(tab.ID)
		return err == nil && count >= uint64(len("active-record"))
	})
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if after := descriptorCount(t); after != before {
		t.Fatalf("close left %d recording descriptors open", after-before)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("active-record")) {
		t.Fatalf("closed recording = %q, err = %v", data, err)
	}
}

func TestStopRecordingIncludesFeedVisibleBeforeStop(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, ReconnectBackoff: []time.Duration{0}})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "recording-stop", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			frame, err := receiver.Next(ctx)
			cancel()
			if err != nil {
				return
			}
			_ = receiver.Ack(frame.Sequence)
		}
	}()
	path := filepath.Join(t.TempDir(), "stop.log")
	if err := manager.StartRecording(tab.ID, path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		marker := []byte(fmt.Sprintf("stop-%06d|", i))
		if err := channel.emit(marker); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !bytes.Contains(tab.terminal.Dump(128), marker) {
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: marker never reached the terminal", i)
			}
		}
		recorded, err := manager.StopRecording(tab.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recorded != uint64(len(marker)) {
			t.Fatalf("iteration %d: stop recorded %d bytes, want %d", i, recorded, len(marker))
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, marker) {
			t.Fatalf("iteration %d: recording = %q, err = %v", i, data, err)
		}
		if err := manager.StartRecording(tab.ID, path); err != nil {
			t.Fatal(err)
		}
	}
}

func descriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := filepath.Glob("/dev/fd/*")
	if err != nil {
		t.Skipf("open descriptor counting unavailable: %v", err)
	}
	return len(entries)
}
