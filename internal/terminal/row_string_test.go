package terminal

import (
	"math/rand"
	"strings"
	"testing"
)

// referenceRowString is the previous full-scan implementation, kept as the oracle for the high-water-mark version.
func referenceRowString(r *row) string {
	last := -1
	for i := len(r.cells) - 1; i >= 0; i-- {
		c := &r.cells[i]
		if c.cont {
			continue
		}
		if (c.r != 0 && c.r != ' ') || len(c.comb) > 0 {
			last = i
			break
		}
	}
	if last < 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i <= last; i++ {
		c := &r.cells[i]
		if c.cont {
			if i > 0 && r.cells[i-1].wide {
				continue
			}
			b.WriteByte(' ')
			continue
		}
		if c.r == 0 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(c.r)
		}
		for _, cr := range c.comb {
			b.WriteRune(cr)
		}
	}
	return b.String()
}

func TestRowStringMatchesReferenceOnGeneratedRows(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	runes := []rune{0, ' ', 'a', 'Z', '~', 'é', '中', '😀'}
	combs := []rune{0x301, 0x200d, 0x20e3}
	for n := 0; n < 5000; n++ {
		cols := 1 + rng.Intn(40)
		r := newRow(cols)
		for i := 0; i < cols; i++ {
			c := &r.cells[i]
			c.r = runes[rng.Intn(len(runes))]
			switch rng.Intn(6) {
			case 0:
				c.comb = []rune{combs[rng.Intn(len(combs))]}
			case 1:
				if i+1 < cols {
					c.r, c.wide = '中', true
					r.cells[i+1] = cell{cont: true}
					i++
				}
			case 2:
				c.cont = true // orphan continuation cell
			}
		}
		r.hi = len(r.cells)
		if got, want := r.String(), referenceRowString(r); got != want {
			t.Fatalf("row %d: got %q want %q", n, got, want)
		}
	}
}

func TestRowStringMatchesReferenceAfterScreenOps(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	pieces := []string{"a", "bc", " ", "中", "é", "́", "😀", "\r", "\n", "\b", "\t",
		"\x1b[2K", "\x1b[K", "\x1b[1K", "\x1b[3@", "\x1b[2P", "\x1b[4X", "\x1b[H", "\x1b[5;7H", "\x1b[4h", "\x1b[4l",
		"\x1b[2J", "\x1b[1S", "\x1b[1T", "\x1b[2L", "\x1b[2M", "\x1b[?7l", "\x1b[?7h", "\x1b[10D", "\x1b[3C", "\x1b[0m"}
	for n := 0; n < 300; n++ {
		s := newScreen(5+rng.Intn(20), 3+rng.Intn(6), 50)
		for step := 0; step < 150; step++ {
			s.write([]byte(pieces[rng.Intn(len(pieces))]))
			if step%40 == 39 {
				s.resize(4+rng.Intn(25), 3+rng.Intn(6))
			}
			for i, r := range s.grid.rows {
				if got, want := r.String(), referenceRowString(r); got != want {
					t.Fatalf("iter %d step %d row %d: got %q want %q", n, step, i, got, want)
				}
				for j := r.hi; j < len(r.cells); j++ {
					if r.cells[j].r != 0 || r.cells[j].comb != nil || r.cells[j].wide || r.cells[j].cont {
						t.Fatalf("iter %d step %d row %d: non-empty cell %d beyond hi %d", n, step, i, j, r.hi)
					}
				}
			}
		}
	}
}
