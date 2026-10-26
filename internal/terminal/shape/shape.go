// Package shape recognises generic interactive prompt shapes on a terminal
// screen snapshot: yes/no confirmations, vertical choice menus and trailing
// questions. It matches layout and punctuation only, never application
// specific wording, titles or spinners, so it needs no per-program upkeep.
//
// The detector is a pure function of the snapshot. It does not know whether
// output is still streaming; callers must only treat a result as a prompt
// once the session output has been idle for a while.
//
// Rules, tried in this order:
//
//   - The screen is first clipped to the pane or frame holding the cursor: a
//     vertical border glyph left or right of the cursor that runs through at
//     least three rows bounds the region.
//   - yes/no: the text before the cursor ends with a bracketed group such as
//     "[y/N]", "(yes/no/[fingerprint])" or "[y,n,q,?]" that offers both a yes
//     and a no answer, followed only by prompt punctuation; nothing follows
//     the cursor. The group may also end the row above when the cursor row
//     holds only an input marker such as ">".
//   - choice: two or more rows starting in the same column within 24 rows of
//     the cursor, either numbered consecutively from 0 or 1 ("1.", "1)",
//     "[1]", "(1)") or carrying exactly one selection pointer ("❯", "›", ">",
//     "*", ...) or one filled radio bullet among hollow ones. A menu with a
//     selection must have the cursor within 4 rows above to 6 rows below it
//     and at most 4 non-blank rows below; a cursor below it must not follow a
//     shell prompt ("$", "#", "%"), and "*" or ">" lists need the cursor at
//     the start of its row there. A numbered list without a selection needs
//     the cursor on a reply line ending in "?" or ":" at most two rows below
//     it, with nothing below.
//   - question: the text before the cursor ends with "?" or ":" (optionally
//     followed by a bracketed default such as "(demo)"), contains a letter,
//     the cursor is at most two columns after it, and every row below is
//     blank; or the cursor row is an input marker under such a row.
package shape

import (
	"hash/fnv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// Kind classifies a detected prompt. The kinds are ordered by confidence:
// yes/no and choice prompts have explicit structure, a question only has a
// trailing "?" or ":" next to the cursor.
type Kind string

const (
	KindNone     Kind = "none"
	KindYesNo    Kind = "yesno"
	KindChoice   Kind = "choice"
	KindQuestion Kind = "question"
)

// Option is one answer the prompt offers.
//
// Keys is the text that selects the option directly, without any trailing
// Enter: "y" or "no" for a yes/no prompt, the item number for a numbered
// menu. It is empty when the option can only be reached by navigation.
// Submit reports whether the shape suggests the program reads a whole line,
// so Keys must be followed by Enter: true for yes/no prompts and for
// numbered lists without a selection marker (both are read line-buffered by
// almost every program), false for digits in a menu with a selection marker,
// which such menus usually treat as a hotkey. A program that reads a single
// key for a yes/no prompt receives a stray Enter; callers that cannot accept
// that should send Keys alone and let the user confirm.
type Option struct {
	Label   string `json:"label"`
	Keys    string `json:"keys,omitempty"`
	Submit  bool   `json:"submit,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// Prompt is the detector result.
//
// CursorIndex is the option carrying the selection marker, or -1 when the
// shape has none. When it is >= 0 the menu is navigable: moving
// (index - CursorIndex) steps with Down (Up when negative) and pressing Enter
// selects an option and works for numbered and unnumbered menus alike, so it
// is the preferred encoding for marked menus.
//
// Fingerprint hashes the kind and the text of the prompt region (question
// line and option rows, including the selection marker). It changes when the
// prompt, its options or its selection change and stays the same while
// unrelated rows, such as a spinner elsewhere on the screen, redraw. Two
// identical prompts shown one after another share a fingerprint, so a caller
// keeping a prompt epoch must also advance it when the prompt disappears.
type Prompt struct {
	Kind        Kind     `json:"kind"`
	Question    string   `json:"question,omitempty"`
	Options     []Option `json:"options,omitempty"`
	CursorIndex int      `json:"cursorIndex"`
	Fingerprint uint64   `json:"fingerprint,omitempty"`
	// WeakMarker reports a choice whose selection pointer is "*" or ">",
	// glyphs that also start ordinary output such as branch listings or
	// quoted text.
	WeakMarker bool `json:"weakMarker,omitempty"`
}

func none() Prompt { return Prompt{Kind: KindNone, CursorIndex: -1} }

// Detect inspects a screen snapshot and reports the prompt shape around the
// cursor, or KindNone.
func Detect(snap terminal.Snapshot) Prompt {
	return DetectLines(snap.Lines, snap.CursorRow, snap.CursorCol)
}

// DetectLines is Detect for raw screen rows as produced by
// terminal.Snapshot.Lines: one string per row, wide characters occupying two
// display columns, combining marks attached to their base character.
// cursorRow and cursorCol are zero-based cell coordinates.
func DetectLines(lines []string, cursorRow, cursorCol int) Prompt {
	if cursorRow < 0 || cursorCol < 0 {
		return none()
	}
	rows := make([]string, max(len(lines), cursorRow+1))
	copy(rows, lines)
	sc := newScreen(rows, cursorRow, cursorCol)
	sc = sc.clipPane()
	if p, ok := sc.yesNo(); ok {
		return p
	}
	if p, ok := sc.choice(); ok {
		return p
	}
	if p, ok := sc.question(); ok {
		return p
	}
	return none()
}

func fingerprint(kind Kind, region []string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(kind))
	for _, r := range region {
		h.Write([]byte{0})
		h.Write([]byte(strings.TrimSpace(r)))
	}
	return h.Sum64()
}
