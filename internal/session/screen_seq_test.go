package session

import (
	"context"
	"testing"
)

// TestTabScreenSeqMonotonicAndOutputSince drives output through the tab feed
// path and asserts the ring-backed sequence anchors paged reads: the sequence
// only advances, OutputSince returns exactly the bytes after an anchor, and
// re-reading from the latest sequence yields nothing.
func TestTabScreenSeqMonotonicAndOutputSince(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: terminalFactory{},
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "seq-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "seq-channel")
	channel := connector.transport(0).channel(0)

	if seq := tab.ScreenSeq(); seq != 0 {
		t.Fatalf("fresh ScreenSeq = %d, want 0", seq)
	}
	if err := channel.emit([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 5 })
	if err := channel.emit([]byte(" world")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return tab.ScreenSeq() == 11 })

	managerSeq, err := manager.ScreenSeq(tab.ID)
	if err != nil || managerSeq != 11 {
		t.Fatalf("manager.ScreenSeq = %d, %v; want 11", managerSeq, err)
	}
	data, start, latest, err := manager.OutputSince(tab.ID, 5, 100)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != " world" || start != 5 || latest != 11 {
		t.Fatalf("OutputSince(5) = %q, start %d, latest %d; want \" world\", 5, 11", data, start, latest)
	}

	data, start, latest, err = manager.OutputSince(tab.ID, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hell" || start != 0 || latest != 11 {
		t.Fatalf("OutputSince(0, 4) = %q, start %d, latest %d; want \"hell\", 0, 11", data, start, latest)
	}

	data, start, latest, err = manager.OutputSince(tab.ID, latest, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 || start != 11 || latest != 11 {
		t.Fatalf("OutputSince(latest) = %q, start %d, latest %d; want empty, 11, 11", data, start, latest)
	}
}

// TestTabScreenSeqZeroWithoutRingTerminal pins the fallback: a terminal state
// without a sequence-tracked ring reports 0 so callers stay on the unanchored
// path.
func TestTabScreenSeqZeroWithoutRingTerminal(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector,
		Terminals: newFakeTerminalFactory(),
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "fake-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "fake-channel")
	if seq := tab.ScreenSeq(); seq != 0 {
		t.Fatalf("ScreenSeq without ring = %d, want 0", seq)
	}
	if _, _, _, err := manager.OutputSince(tab.ID, 0, 10); err != ErrUnsupported {
		t.Fatalf("OutputSince without ring err = %v, want ErrUnsupported", err)
	}
}
