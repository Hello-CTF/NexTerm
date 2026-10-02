package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
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

func descriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := filepath.Glob("/dev/fd/*")
	if err != nil {
		t.Skipf("open descriptor counting unavailable: %v", err)
	}
	return len(entries)
}
