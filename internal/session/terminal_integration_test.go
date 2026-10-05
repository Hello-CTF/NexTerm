package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCoreTerminalRawReplayAndResponseIntegration(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, ReconnectBackoff: []time.Duration{0}})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "core-terminal", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	recordingPath := filepath.Join(t.TempDir(), "recording.log")
	if err := manager.StartRecording(tab.ID, recordingPath); err != nil {
		t.Fatal(err)
	}
	raw := []byte{0xff, 0x00, 'r', 'a', 'w'}
	if err := channel.emit(raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(tab.terminal.Dump(1024), raw) })
	if dump, err := manager.RawDump(tab.ID, 1024); err != nil || !bytes.Contains(dump, raw) {
		t.Fatalf("shared raw dump = %v, err = %v", dump, err)
	}
	waitFor(t, func() bool {
		current, err := manager.RecordingBytes(tab.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current >= uint64(len(raw))
	})
	recorded, err := manager.StopRecording(tab.ID)
	if err != nil || recorded < uint64(len(raw)) {
		t.Fatalf("stopped recording bytes = %d, err = %v", recorded, err)
	}
	recording, err := os.ReadFile(recordingPath)
	if err != nil || !bytes.Contains(recording, raw) {
		t.Fatalf("recording = %v, err = %v", recording, err)
	}
	exportPath := filepath.Join(t.TempDir(), "export.log")
	if exported, err := manager.ExportLog(tab.ID, exportPath, 1024); err != nil || exported < uint64(len(raw)) {
		t.Fatalf("exported bytes = %d, err = %v", exported, err)
	}
	if snapshot, err := manager.TerminalSnapshot(tab.ID); err != nil || snapshot.Cols != 80 || snapshot.Rows != 24 {
		t.Fatalf("shared snapshot = %+v, err = %v", snapshot, err)
	}
	if _, err := manager.ScreenText(tab.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.TailLines(tab.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.TerminalModeState(tab.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.SwitchEncoding(tab.ID, "utf8"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetVisible(tab.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetVisible(tab.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	receiver := bindTestReceiver(t, manager, "b-1")
	assertFrameData(t, receiver, replayClear)
	frame := receiveTestFrame(t, receiver)
	if !bytes.Contains(frame.Data, raw) {
		t.Fatalf("production terminal altered raw replay: %v", frame.Data)
	}
	if err := channel.emit([]byte("\x1b[6n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		written := channel.written()
		return bytes.Contains(written, []byte("\x1b[")) && bytes.HasSuffix(written, []byte("R"))
	})
	if err := manager.WriteInternal(context.Background(), tab.ID, []byte("internal")); err != nil {
		t.Fatal(err)
	}
	if got := channel.written(); !bytes.HasSuffix(got, []byte("internal")) {
		t.Fatalf("trusted internal write = %q", got)
	}
}

func TestCoreTerminalRejectsUnknownEncodingBeforeOpeningPTY(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "encoding", Kind: KindSSH, Encoding: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: session.ID, ChannelID: "a-1", Cols: 80, Rows: 24,
	}); err == nil {
		t.Fatal("unknown terminal encoding was accepted")
	}
	if got := connector.transport(0).channelCount(); got != 0 {
		t.Fatalf("encoding failure opened %d PTYs", got)
	}
}
