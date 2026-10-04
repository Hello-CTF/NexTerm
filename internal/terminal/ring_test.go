package terminal

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

func TestRingKeepsRecentBytesOnly(t *testing.T) {
	r := NewRing(16)
	r.Push([]byte("0123456789"))
	r.Push([]byte("ABCDEFGHIJ"))
	if got := r.Dump(0); !bytes.Equal(got, []byte("456789ABCDEFGHIJ")) {
		t.Fatalf("snapshot = %q", got)
	}
	if r.Total() != 20 || r.Dropped() != 4 || r.Len() != 16 {
		t.Fatalf("total=%d dropped=%d len=%d", r.Total(), r.Dropped(), r.Len())
	}
	if r.Base() != 4 {
		t.Fatalf("base = %d, want 4", r.Base())
	}
}

func TestRingChunkLargerThanCapKeepsTail(t *testing.T) {
	r := NewRing(8)
	input := []byte("a very long stream of bytes exceeding cap")
	r.Push(input)
	if got := r.Dump(0); !bytes.Equal(got, []byte("ding cap")) {
		t.Fatalf("snapshot = %q", got)
	}
	if r.Dropped() != uint64(len(input)-8) {
		t.Fatalf("dropped = %d, want %d", r.Dropped(), len(input)-8)
	}
}

func TestRingDumpRespectsMax(t *testing.T) {
	r := NewRing(1024)
	r.Push(bytes.Repeat([]byte{'x'}, 600))
	r.Push(bytes.Repeat([]byte{'y'}, 300))
	d := r.Dump(100)
	if len(d) != 100 || d[0] != 'y' || d[len(d)-1] != 'y' {
		t.Fatalf("dump len=%d first=%c last=%c", len(d), d[0], d[len(d)-1])
	}
	if got := r.Dump(0); len(got) != 900 {
		t.Fatalf("dump(0) len = %d, want 900", len(got))
	}
	if got := r.Dump(-5); len(got) != 900 {
		t.Fatalf("dump(-5) len = %d, want 900", len(got))
	}
}

func TestRingReplayFrom(t *testing.T) {
	r := NewRing(8)
	r.Push([]byte("0123456789"))

	data, start := r.ReplayFrom(4, 0)
	if start != 4 || !bytes.Equal(data, []byte("456789")) {
		t.Fatalf("replay(4) start=%d data=%q", start, data)
	}
	data, start = r.ReplayFrom(0, 0)
	if start != 2 || !bytes.Equal(data, []byte("23456789")) {
		t.Fatalf("replay(0) start=%d data=%q", start, data)
	}
	data, start = r.ReplayFrom(6, 2)
	if start != 6 || !bytes.Equal(data, []byte("67")) {
		t.Fatalf("replay(6,2) start=%d data=%q", start, data)
	}
	data, start = r.ReplayFrom(99, 0)
	if start != 10 || len(data) != 0 {
		t.Fatalf("replay(99) start=%d data=%q", start, data)
	}
}

func TestRingClearKeepsCounters(t *testing.T) {
	r := NewRing(8)
	r.Push([]byte("0123456789"))
	r.Clear()
	if r.Len() != 0 || r.Total() != 10 || r.Dropped() != 2 || r.Base() != 10 {
		t.Fatalf("after clear: len=%d total=%d dropped=%d base=%d", r.Len(), r.Total(), r.Dropped(), r.Base())
	}
	r.Push([]byte("ab"))
	if got := r.Dump(0); !bytes.Equal(got, []byte("ab")) {
		t.Fatalf("snapshot after clear = %q", got)
	}
}

func TestRingAgainstReferenceModel(t *testing.T) {
	for _, cap := range []int{1, 2, 3, 7, 8, 16, 64, 1000} {
		t.Run(fmt.Sprintf("cap%d", cap), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(cap)))
			r := NewRing(cap)
			var model []byte
			var total uint64
			for step := 0; step < 2000; step++ {
				n := rng.Intn(cap*2 + 4)
				chunk := make([]byte, n)
				for i := range chunk {
					chunk[i] = byte(rng.Intn(256))
				}
				r.Push(chunk)
				total += uint64(n)
				model = append(model, chunk...)
				if len(model) > cap {
					model = model[len(model)-cap:]
				}

				if !bytes.Equal(r.Dump(0), model) {
					t.Fatalf("step %d: snapshot mismatch (len=%d cap=%d)", step, r.Len(), cap)
				}
				if r.Total() != total {
					t.Fatalf("step %d: total=%d want %d", step, r.Total(), total)
				}
				if r.Dropped() != total-uint64(len(model)) {
					t.Fatalf("step %d: dropped=%d want %d", step, r.Dropped(), total-uint64(len(model)))
				}
				if max := rng.Intn(cap + 2); max >= 0 {
					want := model
					if max > 0 && len(want) > max {
						want = want[len(want)-max:]
					}
					if got := r.Dump(max); !bytes.Equal(got, want) {
						t.Fatalf("step %d: dump(%d) mismatch", step, max)
					}
				}
				seq := total/2 + uint64(rng.Intn(cap+2))
				data, start := r.ReplayFrom(seq, 0)
				wantStart := seq
				base := total - uint64(len(model))
				if wantStart < base {
					wantStart = base
				}
				if wantStart > total {
					wantStart = total
				}
				if start != wantStart || !bytes.Equal(data, model[start-base:]) {
					t.Fatalf("step %d: replay(%d) start=%d want %d", step, seq, start, wantStart)
				}
			}
		})
	}
}
