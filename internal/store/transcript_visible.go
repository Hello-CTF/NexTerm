package store

import (
	"bytes"
	"sort"
	"unicode/utf8"
)

const (
	visibleWindowBytes   = 16 << 10
	visiblePreviewRadius = 80
)

type visibleSeqMark struct {
	offset int
	seq    int64
	ts     int64
}

type visibleMatcher struct {
	query      []byte
	window     []byte
	marks      []visibleSeqMark
	pending    []byte
	matches    []TranscriptMatch
	maxMatches int
	reportFrom int
}

func newVisibleMatcher(query []byte, maxMatches int) *visibleMatcher {
	return &visibleMatcher{query: query, maxMatches: maxMatches}
}

func (m *visibleMatcher) full() bool {
	return len(m.matches) >= m.maxMatches
}

func (m *visibleMatcher) consume(seq, ts int64, data []byte) {
	raw := m.pending
	m.pending = nil
	raw = append(raw, data...)
	visible, tail := visibleBytes(raw)
	m.pending = tail
	if len(visible) == 0 {
		return
	}
	m.marks = append(m.marks, visibleSeqMark{offset: len(m.window), seq: seq, ts: ts})
	m.window = append(m.window, visible...)
	m.searchNew()
	m.trim()
}

func (m *visibleMatcher) searchNew() {
	from := m.reportFrom
	window := m.window
	query := m.query
	for from <= len(window)-len(query) && !m.full() {
		index := bytes.Index(window[from:], query)
		if index < 0 {
			break
		}
		start := from + index
		m.matches = append(m.matches, TranscriptMatch{
			Seq:     m.seqAt(start),
			TS:      m.tsAt(start),
			Preview: transcriptPreview(window, start, len(query)),
		})
		from = start + len(query)
	}
	m.reportFrom = len(window) - len(query) + 1
	if m.reportFrom < 0 {
		m.reportFrom = 0
	}
}

func (m *visibleMatcher) seqAt(offset int) int64 {
	return m.markAt(offset).seq
}

func (m *visibleMatcher) tsAt(offset int) int64 {
	return m.markAt(offset).ts
}

func (m *visibleMatcher) markAt(offset int) visibleSeqMark {
	index := sort.Search(len(m.marks), func(i int) bool { return m.marks[i].offset > offset }) - 1
	if index < 0 {
		return visibleSeqMark{}
	}
	return m.marks[index]
}

func (m *visibleMatcher) trim() {
	if len(m.window) <= visibleWindowBytes {
		return
	}
	drop := len(m.window) - visibleWindowBytes
	m.window = append([]byte(nil), m.window[drop:]...)
	var carry visibleSeqMark
	kept := make([]visibleSeqMark, 0, len(m.marks))
	for _, mark := range m.marks {
		if mark.offset < drop {
			carry = mark
			continue
		}
		kept = append(kept, visibleSeqMark{offset: mark.offset - drop, seq: mark.seq, ts: mark.ts})
	}
	if len(kept) == 0 || kept[0].offset > 0 {
		kept = append([]visibleSeqMark{{offset: 0, seq: carry.seq, ts: carry.ts}}, kept...)
	}
	m.marks = kept
	if m.reportFrom > drop {
		m.reportFrom -= drop
	} else {
		m.reportFrom = 0
	}
}

func transcriptPreview(data []byte, matchStart, matchLen int) []byte {
	start := matchStart - visiblePreviewRadius
	if start < 0 {
		start = 0
	}
	end := matchStart + matchLen + visiblePreviewRadius
	if end > len(data) {
		end = len(data)
	}
	return bytes.Clone(data[start:end])
}

func visibleBytes(raw []byte) (visible []byte, tail []byte) {
	index := 0
	for index < len(raw) {
		current := raw[index]
		switch {
		case current == 0x1b:
			consumed, complete := escapeSequence(raw[index:])
			if !complete {
				return visible, raw[index:]
			}
			index += consumed
		case current == 0x9b:
			consumed, complete := csiSequence(raw[index+1:])
			if !complete {
				return visible, raw[index:]
			}
			index += 1 + consumed
		case current == 0x90 || current == 0x98 || current == 0x9e || current == 0x9f:
			consumed, complete := controlString(raw[index+1:])
			if !complete {
				return visible, raw[index:]
			}
			index += 1 + consumed
		default:
			r, size := utf8.DecodeRune(raw[index:])
			if r == utf8.RuneError && size <= 1 {
				if !utf8.FullRune(raw[index:]) {
					return visible, raw[index:]
				}
				visible = append(visible, current)
				index++
				continue
			}
			if r >= 0x80 && r <= 0x9f {
				switch r {
				case 0x9b:
					consumed, complete := csiSequence(raw[index+size:])
					if !complete {
						return visible, raw[index:]
					}
					index += size + consumed
				case 0x90, 0x98, 0x9e, 0x9f:
					consumed, complete := controlString(raw[index+size:])
					if !complete {
						return visible, raw[index:]
					}
					index += size + consumed
				default:
					index += size
				}
				continue
			}
			visible = append(visible, raw[index:index+size]...)
			index += size
		}
	}
	return visible, nil
}

func escapeSequence(raw []byte) (consumed int, complete bool) {
	if len(raw) < 2 {
		return 0, false
	}
	switch raw[1] {
	case '[':
		consumed, complete := csiSequence(raw[2:])
		return 2 + consumed, complete
	case ']':
		consumed, complete := oscSequence(raw[2:])
		return 2 + consumed, complete
	case 'P', 'X', '^', '_':
		consumed, complete := controlString(raw[2:])
		return 2 + consumed, complete
	default:
		index := 1
		for index < len(raw) && raw[index] >= 0x20 && raw[index] <= 0x2f {
			index++
		}
		if index >= len(raw) {
			return 0, false
		}
		if raw[index] >= 0x30 && raw[index] <= 0x7e {
			return index + 1, true
		}
		return index, true
	}
}

func controlString(raw []byte) (consumed int, complete bool) {
	for index := 0; index < len(raw); index++ {
		if raw[index] == 0x1b && index+1 < len(raw) && raw[index+1] == '\\' {
			return index + 2, true
		}
		if raw[index] == 0x9c {
			return index + 1, true
		}
	}
	return 0, false
}

func csiSequence(raw []byte) (consumed int, complete bool) {
	for index, current := range raw {
		if current >= 0x40 && current <= 0x7e {
			return index + 1, true
		}
		if current < 0x20 || current > 0x3f {
			return index, true
		}
	}
	return 0, false
}

func oscSequence(raw []byte) (consumed int, complete bool) {
	for index := 0; index < len(raw); index++ {
		if raw[index] == 0x07 {
			return index + 1, true
		}
		if raw[index] == 0x1b && index+1 < len(raw) && raw[index+1] == '\\' {
			return index + 2, true
		}
	}
	return 0, false
}
