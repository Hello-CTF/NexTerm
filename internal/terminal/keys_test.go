package terminal

import (
	"bytes"
	"testing"
)

func TestEncodeSendSimple(t *testing.T) {
	if got := EncodeSend("y", true); !bytes.Equal(got, []byte("y\r")) {
		t.Fatalf("got %q", got)
	}
	if got := EncodeSend("", true); !bytes.Equal(got, []byte("\r")) {
		t.Fatalf("got %q", got)
	}
	if got := EncodeSend("ls", false); !bytes.Equal(got, []byte("ls")) {
		t.Fatalf("got %q", got)
	}
}

func TestEncodeSendSpecialKeys(t *testing.T) {
	if got := EncodeSend("<ctrl+c>", false); !bytes.Equal(got, []byte{0x03}) {
		t.Fatalf("got %q", got)
	}
	if got := EncodeSend("<up><up><enter>", false); !bytes.Equal(got, []byte("\x1b[A\x1b[A\r")) {
		t.Fatalf("got %q", got)
	}
	if got := EncodeSend("sudo <tab>", false); !bytes.Equal(got, []byte("sudo \t")) {
		t.Fatalf("got %q", got)
	}
	if got := EncodeSend("vim /etc/hosts<enter>", false); !bytes.Equal(got, []byte("vim /etc/hosts\r")) {
		t.Fatalf("got %q", got)
	}
	if got := EncodeSend(":wq<enter>", true); !bytes.Equal(got, []byte(":wq\r\r")) {
		t.Fatalf("got %q", got)
	}
}

func TestEncodeSendUnknownAndCase(t *testing.T) {
	if got := EncodeSend("<notakey>", false); !bytes.Equal(got, []byte("<notakey>")) {
		t.Fatalf("unknown tag should pass through: %q", got)
	}
	if got := EncodeSend("<CTRL+C>", false); !bytes.Equal(got, []byte{0x03}) {
		t.Fatalf("got %q", got)
	}
	if got := EncodeKey(" Enter "); !bytes.Equal(got, []byte("\r")) {
		t.Fatalf("got %q", got)
	}
	if got := EncodeKey("nope"); got != nil {
		t.Fatalf("unknown key = %q, want nil", got)
	}
}

func TestEncodeSendAngleBracketQuirk(t *testing.T) {
	if got := EncodeSend("a>b<tab>", false); !bytes.Equal(got, []byte("a>b<tab>")) {
		t.Fatalf("got %q", got)
	}
	if got := EncodeSend("x<y", false); !bytes.Equal(got, []byte("x<y")) {
		t.Fatalf("got %q", got)
	}
}

func TestKeyTableFull(t *testing.T) {
	want := map[string]string{
		"enter": "\r", "return": "\r", "linefeed": "\n", "lf": "\n",
		"tab": "\t", "shift+tab": "\x1b[Z", "backtab": "\x1b[Z",
		"esc": "\x1b", "escape": "\x1b", "space": " ", "backspace": "\x7f",
		"delete": "\x1b[3~", "del": "\x1b[3~", "insert": "\x1b[2~",
		"home": "\x1b[H", "end": "\x1b[F",
		"pageup": "\x1b[5~", "pagedown": "\x1b[6~",
		"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
		"ctrl+@": "\x00", "ctrl+space": "\x00",
		"ctrl+\\": "\x1c", "ctrl+]": "\x1d", "ctrl+^": "\x1e", "ctrl+_": "\x1f",
		"f1": "\x1bOP", "f2": "\x1bOQ", "f3": "\x1bOR", "f4": "\x1bOS",
		"f5": "\x1b[15~", "f6": "\x1b[17~", "f7": "\x1b[18~", "f8": "\x1b[19~",
		"f9": "\x1b[20~", "f10": "\x1b[21~", "f11": "\x1b[23~", "f12": "\x1b[24~",
	}
	for c := byte('a'); c <= 'z'; c++ {
		want["ctrl+"+string(c)] = string([]byte{c - 'a' + 1})
	}
	if len(keySequences) != len(want) {
		t.Fatalf("table size = %d, want %d", len(keySequences), len(want))
	}
	for name, seq := range want {
		if got := EncodeKey(name); !bytes.Equal(got, []byte(seq)) {
			t.Fatalf("EncodeKey(%q) = %q, want %q", name, got, seq)
		}
	}
}
