package terminal

import "testing"

func TestEmulatorTracksVisibleScreenOnly(t *testing.T) {
	e := NewEmulator(20, 3)
	e.Write([]byte("one\r\ntwo\r\nthree\r\nfour"))
	snap := e.Snapshot()
	want := []string{"two", "three", "four"}
	if len(snap.Lines) != len(want) {
		t.Fatalf("lines = %q", snap.Lines)
	}
	for i := range want {
		if snap.Lines[i] != want[i] {
			t.Fatalf("lines = %q, want %q", snap.Lines, want)
		}
	}
	if snap.CursorRow != 2 || snap.CursorCol != 4 || snap.Cols != 20 || snap.Rows != 3 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if e.scr.scrollbackLen() != 0 {
		t.Fatal("emulator kept scrollback")
	}
	// Status requests are answered by nobody and must not accumulate.
	for i := 0; i < 100; i++ {
		e.Write([]byte("\x1b[6n\x1b[c"))
	}
	if len(e.scr.responses) != 0 {
		t.Fatalf("pending responses = %d bytes", len(e.scr.responses))
	}
	e.Write([]byte("\x1b[?1049h"))
	if !e.Snapshot().AltScreen {
		t.Fatal("alternate screen not tracked")
	}
	e.Resize(40, 10)
	e.Resize(0, 10)
	if snap := e.Snapshot(); snap.Cols != 40 || snap.Rows != 10 {
		t.Fatalf("after resize %dx%d", snap.Cols, snap.Rows)
	}
}

func BenchmarkEmulatorWrite(b *testing.B) {
	chunk := []byte("\x1b[32mok\x1b[0m compiling module 42 of 120 ......................................\r\n")
	e := NewEmulator(120, 40)
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	for b.Loop() {
		e.Write(chunk)
	}
}
