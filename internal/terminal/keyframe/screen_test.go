package keyframe

import (
	"bytes"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestScreenSurvivesArbitraryInput feeds random bytes, escape fragments and
// resizes: the screen runs on recorded output and must never panic.
func TestScreenSurvivesArbitraryInput(t *testing.T) {
	fragments := []string{
		"\x1b[", "\x1b]", "\x1bP", "\x1b(", "\x1b)0", "\x0e", "\x0f", "\x1b#8", "\x1b[?1049h", "\x1b[?1049l",
		"\x1b[?47h", "\x1b[38:2::1:2:3m", "\x1b[4:3m", "\x1b[999;999H", "\x1b[0;0r", "\x1b[99b", "中", "e\u0301",
		"\x1b[3J", "\x1b[2J", "\x1b[99L", "\x1b[99M", "\x1b[99P", "\x1b[99@", "\x1b[99X", "\x1b[99S", "\x1b[99T",
		"\x1b7", "\x1b8", "\x1bM", "\x1bD", "\x1bE", "\x1bH", "\x1b[3g", "\x1b[?6h", "\x1b[?7l", "\x1b[4h", "\x1bc",
	}
	for seed := uint64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewPCG(seed, 7))
		screen := New(2+rng.IntN(40), 1+rng.IntN(12))
		for step := 0; step < 60; step++ {
			switch rng.IntN(6) {
			case 0:
				screen.Resize(2+rng.IntN(60), 1+rng.IntN(20))
			case 1:
				raw := make([]byte, rng.IntN(64))
				for i := range raw {
					raw[i] = byte(rng.IntN(256))
				}
				screen.Write(raw)
			default:
				var b strings.Builder
				for n := rng.IntN(8); n >= 0; n-- {
					b.WriteString(fragments[rng.IntN(len(fragments))])
					b.WriteString(strings.Repeat("x", rng.IntN(30)))
				}
				screen.Write([]byte(b.String()))
			}
		}
		if state := screen.State(); state == "" && len(screen.normal.lines) == 0 {
			t.Fatalf("seed %d: empty screen", seed)
		}
	}
}

// TestScrollbackIsBounded keeps the modelled scrollback at the replay view's.
func TestScrollbackIsBounded(t *testing.T) {
	screen := New(20, 5)
	screen.Write(bytes.Repeat([]byte("line\r\n"), Scrollback+500))
	if got := len(screen.normal.lines); got != Scrollback+5 {
		t.Fatalf("lines = %d, want %d", got, Scrollback+5)
	}
}

func BenchmarkScreenWrite(b *testing.B) {
	var chunk bytes.Buffer
	for i := 0; i < 200; i++ {
		chunk.WriteString("\x1b[1;32muser@host\x1b[0m:\x1b[34m~/src\x1b[0m$ ls -la 中文 file\r\n")
		chunk.WriteString("drwxr-xr-x  5 user group 4096 Nov 13 09:00 \x1b[01;34mdirectory\x1b[0m\r\n")
	}
	data := chunk.Bytes()
	screen := New(120, 40)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		screen.Write(data)
	}
}

func BenchmarkScreenState(b *testing.B) {
	screen := New(120, 40)
	for i := 0; i < Scrollback/10; i++ {
		screen.Write([]byte("\x1b[1;32muser@host\x1b[0m:\x1b[34m~/src\x1b[0m$ ls -la 中文 file and more text\r\n"))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = screen.State()
	}
}
