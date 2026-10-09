package shellintegr

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

func osc7(payload string) []byte {
	return []byte("\x1b]7;" + payload + "\x1b\\")
}

func TestObserveReportsCWD(t *testing.T) {
	tests := []struct {
		name    string
		stream  []byte
		wantCWD string
	}{
		{"st terminated", osc7("file://host/tmp"), "/tmp"},
		{"bel terminated", []byte("\x1b]7;file://host/tmp\x07"), "/tmp"},
		{"percent encoded", osc7("file://host/a%20b%23c%3Fd%2Fe"), "/a b#c?d/e"},
		{"raw utf8", osc7("file://host/tmp/caf\u00e9"), "/tmp/caf\u00e9"},
		{"empty host", osc7("file:///tmp"), "/tmp"},
		{"surrounded by output", append(append([]byte("user@host:~$ "), osc7("file://h/home/u")...), []byte(" ls\r\nfile1\r\n")...), "/home/u"},
		{"latest of multiple wins", append(osc7("file://h/first"), osc7("file://h/second")...), "/second"},
		{"non file scheme ignored", append(osc7("https://h/tmp"), osc7("file://h/ok")...), "/ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewTracker()
			if cwd, ok := tr.Observe(tt.stream); !ok || cwd != tt.wantCWD {
				t.Fatalf("Observe() = %q, %v; want %q, true", cwd, ok, tt.wantCWD)
			}
			if tr.CWD() != tt.wantCWD {
				t.Fatalf("CWD() = %q; want %q", tr.CWD(), tt.wantCWD)
			}
		})
	}
}

func TestObserveIgnoresForeignSequences(t *testing.T) {
	stream := []byte("\x1b[2J\x1b]0;window title\x07\x1b]133;A\x1b\\plain \x1b]52;c;Y2xpcGJvYXJk\x07text")
	tr := NewTracker()
	if cwd, ok := tr.Observe(stream); ok {
		t.Fatalf("Observe() = %q, true; want no report", cwd)
	}
	if tr.CWD() != "" {
		t.Fatalf("CWD() = %q; want empty", tr.CWD())
	}
	if cwd, ok := tr.Observe(osc7("file://h/after")); !ok || cwd != "/after" {
		t.Fatalf("Observe() after noise = %q, %v; want /after, true", cwd, ok)
	}
}

func TestObserveAcrossChunkBoundaries(t *testing.T) {
	stream := append(append([]byte("prompt$ "), osc7("file://host/a%20b/c")...), []byte("\r\noutput\r\n")...)
	want := "/a b/c"
	for split := 0; split <= len(stream); split++ {
		tr := NewTracker()
		var got string
		var ok bool
		for _, chunk := range [][]byte{stream[:split], stream[split:]} {
			if cwd, found := tr.Observe(chunk); found {
				got, ok = cwd, true
			}
		}
		if !ok || got != want {
			t.Fatalf("split %d: Observe() = %q, %v; want %q, true", split, got, ok, want)
		}
		if tr.CWD() != want {
			t.Fatalf("split %d: CWD() = %q; want %q", split, tr.CWD(), want)
		}
	}
}

func TestObserveByteByByte(t *testing.T) {
	stream := append(append([]byte("\x1b]0;t\x07x"), osc7("file://h/byte/wise")...), []byte("y")...)
	tr := NewTracker()
	for _, b := range stream {
		tr.Observe([]byte{b})
	}
	if tr.CWD() != "/byte/wise" {
		t.Fatalf("CWD() = %q; want /byte/wise", tr.CWD())
	}
}

func TestObserveOverlongSequenceDropped(t *testing.T) {
	tr := NewTracker()
	giant := append([]byte("\x1b]7;file://h/"), bytes.Repeat([]byte("a"), maxPending+64)...)
	if cwd, ok := tr.Observe(giant); ok {
		t.Fatalf("Observe(overlong) = %q, true; want no report", cwd)
	}
	if cwd, ok := tr.Observe(append([]byte("a\x07"), osc7("file://h/ok")...)); !ok || cwd != "/ok" {
		t.Fatalf("Observe() after overlong = %q, %v; want /ok, true", cwd, ok)
	}
}

func TestObserveDoesNotAlterStream(t *testing.T) {
	var stream []byte
	stream = append(stream, []byte("user@host:~$ cd /a b\r\n")...)
	stream = append(stream, osc7("file://host/a%20b")...)
	stream = append(stream, []byte("\x1b]0;title\x07\r\nuser@host:/a b$ ")...)
	stream = append(stream, osc7("file://host/caf\u00e9")...)
	stream = append(stream, []byte("ls\r\n")...)

	feed := func(t *testing.T, chunks [][]byte) {
		t.Helper()
		tr := NewTracker()
		var recorded bytes.Buffer
		for _, chunk := range chunks {
			tr.Observe(chunk)
			recorded.Write(chunk)
		}
		if !bytes.Equal(recorded.Bytes(), stream) {
			t.Fatal("recorded stream differs from shell output")
		}
		if tr.CWD() != "/caf\u00e9" {
			t.Fatalf("CWD() = %q; want /caf\u00e9", tr.CWD())
		}
	}

	t.Run("single chunk", func(t *testing.T) { feed(t, [][]byte{stream}) })
	t.Run("byte by byte", func(t *testing.T) {
		var chunks [][]byte
		for _, b := range stream {
			chunks = append(chunks, []byte{b})
		}
		feed(t, chunks)
	})
	t.Run("fixed chunks", func(t *testing.T) {
		rest := stream
		var chunks [][]byte
		for len(rest) > 0 {
			n := min(7, len(rest))
			chunks = append(chunks, rest[:n])
			rest = rest[n:]
		}
		feed(t, chunks)
	})
}

func TestRecordingAndReplayPreserveWrapperOutput(t *testing.T) {
	stream := append(append([]byte("\x1b]0;title\x07$ "), osc7("file://host/a%20b")...), []byte("\r\ndone\r\n")...)
	stream = append(stream, osc7("file://host/\xe4\xb8\xad\xe6\x96\x87")...)

	ring := terminal.NewRing(0)
	transcoder := terminal.NewTranscoder(terminal.UTF8)
	tracker := NewTracker()
	var decoded []byte
	for _, b := range stream {
		chunk := []byte{b}
		tracker.Observe(chunk)
		ring.Push(chunk)
		decoded = append(decoded, transcoder.Feed(chunk)...)
	}
	decoded = append(decoded, transcoder.Flush()...)

	if got := ring.Dump(0); !bytes.Equal(got, stream) {
		t.Fatal("ring dump differs from shell output")
	}
	replayed, start := ring.ReplayFrom(0, 0)
	if start != 0 || !bytes.Equal(replayed, stream) {
		t.Fatal("ring replay differs from shell output")
	}
	if !bytes.Equal(decoded, stream) {
		t.Fatal("transcoder output differs from shell output")
	}
	if tracker.CWD() != "/中文" {
		t.Fatalf("CWD() = %q; want /中文", tracker.CWD())
	}
}

func TestParseOSC7EdgeCases(t *testing.T) {
	for _, payload := range []string{
		"7;file://",
		"7;file://host",
		"7;not a url\x00",
		"8;file://host/tmp",
		"7file://host/tmp",
		"7;",
	} {
		if cwd, ok := parseOSC7([]byte(payload)); ok {
			t.Errorf("parseOSC7(%q) = %q, true; want no report", payload, cwd)
		}
	}
}

func BenchmarkObserve(b *testing.B) {
	var stream []byte
	for i := 0; i < 64; i++ {
		stream = append(stream, []byte(fmt.Sprintf("line %d\r\n", i))...)
		stream = append(stream, osc7("file://host/some/dir"+strings.Repeat("x", i%8))...)
	}
	b.SetBytes(int64(len(stream)))
	for b.Loop() {
		tr := NewTracker()
		tr.Observe(stream)
	}
}
