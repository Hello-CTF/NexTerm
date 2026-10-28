package terminal

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func feedString(tab *Tab, s string) {
	tab.Feed([]byte(s))
}

// OSC 2 sets the window title; OSC 0 sets icon name and window title. Both
// must surface through the snapshot and the direct read.
func TestOSCTitleSetAndRead(t *testing.T) {
	tab := newTestTab(t)
	if got := tab.Title(); got != "" {
		t.Fatalf("initial title = %q, want empty", got)
	}
	feedString(tab, "\x1b]2;build log\x07")
	if got := tab.Title(); got != "build log" {
		t.Fatalf("title = %q, want %q", got, "build log")
	}
	if got := tab.Snapshot().Title; got != "build log" {
		t.Fatalf("snapshot title = %q, want %q", got, "build log")
	}
	feedString(tab, "\x1b]0;icon and window\x07")
	if got := tab.Title(); got != "icon and window" {
		t.Fatalf("title after OSC 0 = %q, want %q", got, "icon and window")
	}
}

// A title sequence split across feeds is recognized once the terminating
// chunk arrives, including a split inside a multi-byte character.
func TestOSCTitleSplitAcrossFeeds(t *testing.T) {
	tab := newTestTab(t)
	feedString(tab, "\x1b]2;split ")
	if got := tab.Title(); got != "" {
		t.Fatalf("title set from a partial sequence: %q", got)
	}
	feedString(tab, "ti")
	feedString(tab, "tle\x07")
	if got := tab.Title(); got != "split title" {
		t.Fatalf("title = %q, want %q", got, "split title")
	}

	tab2 := newTestTab(t)
	feedString(tab2, "\x1b]2;caf")
	feedString(tab2, "\xc3")
	feedString(tab2, "\xa9\x07")
	if got := tab2.Title(); got != "café" {
		t.Fatalf("title = %q, want %q", got, "café")
	}
}

// The ESC \ string terminator is accepted alongside BEL.
func TestOSCTitleStringTerminator(t *testing.T) {
	tab := newTestTab(t)
	feedString(tab, "\x1b]2;st title\x1b\\")
	if got := tab.Title(); got != "st title" {
		t.Fatalf("title = %q, want %q", got, "st title")
	}
	feedString(tab, "after")
	if got := tab.Title(); got != "st title" {
		t.Fatalf("title changed by trailing output: %q", got)
	}
	if got := tab.ScreenText(); !strings.Contains(got, "after") {
		t.Fatalf("screen lost trailing output: %q", got)
	}
}

// OSC commands other than 0/2 (icon name, colors, clipboard) never touch the
// tracked title.
func TestOSCTitleIgnoresOtherCommands(t *testing.T) {
	tab := newTestTab(t)
	feedString(tab, "\x1b]2;real title\x07")
	for _, seq := range []string{
		"\x1b]1;icon only\x07",
		"\x1b]3;unused\x07",
		"\x1b]52;c;Y2xpcGJvYXJk\x07",
		"\x1b]11;ff/00/ff\x07",
	} {
		feedString(tab, seq)
	}
	if got := tab.Title(); got != "real title" {
		t.Fatalf("title = %q, want %q", got, "real title")
	}
}

// An oversized title is rejected whole: the previous title stays and the
// parser's bounded data buffer keeps memory in check.
func TestOSCTitleOversizedRejected(t *testing.T) {
	tab := newTestTab(t)
	feedString(tab, "\x1b]2;keep me\x07")
	huge := strings.Repeat("x", maxOSCTitleBytes+1)
	feedString(tab, "\x1b]2;"+huge+"\x07")
	if got := tab.Title(); got != "keep me" {
		t.Fatalf("title = %q, want the previous title kept", got)
	}
	// A title exactly at the limit is accepted.
	edge := strings.Repeat("y", maxOSCTitleBytes)
	feedString(tab, "\x1b]2;"+edge+"\x07")
	if got := tab.Title(); got != edge {
		t.Fatalf("title at the limit = %d bytes, want %d", len(got), maxOSCTitleBytes)
	}
}

// Invalid UTF-8 in a title is sanitized instead of poisoning the snapshot.
func TestOSCTitleInvalidUTF8Sanitized(t *testing.T) {
	tab := newTestTab(t)
	tab.Feed([]byte("\x1b]2;bad \xff\xfe title\x07"))
	got := tab.Title()
	if !utf8.ValidString(got) {
		t.Fatalf("title carries invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "bad") || !strings.Contains(got, "title") {
		t.Fatalf("sanitized title = %q", got)
	}
}

// Resize is a grid operation: the tracked title survives it unchanged.
func TestOSCTitlePersistsAcrossResize(t *testing.T) {
	tab := newTestTab(t)
	feedString(tab, "\x1b]2;resilient\x07")
	if err := tab.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if got := tab.Title(); got != "resilient" {
		t.Fatalf("title after resize = %q", got)
	}
	if got := tab.Snapshot().Title; got != "resilient" {
		t.Fatalf("snapshot title after resize = %q", got)
	}
}

// Replaying recorded output does not re-parse the title on the source tab;
// the same bytes fed to a fresh tab set it there.
func TestOSCTitleReplayDoesNotReparse(t *testing.T) {
	tab := newTestTab(t)
	feedString(tab, "\x1b]2;original\x07")
	replay, _, _ := tab.ReplayFrom(0, 1<<20)
	if len(replay) == 0 {
		t.Fatal("nothing to replay")
	}
	if got := tab.Title(); got != "original" {
		t.Fatalf("title changed by replay read: %q", got)
	}
	fresh := newTestTab(t)
	fresh.Feed(replay)
	if got := fresh.Title(); got != "original" {
		t.Fatalf("replayed title on fresh tab = %q, want %q", got, "original")
	}
}

// An empty payload clears the title, matching xterm behavior.
func TestOSCTitleEmptyClears(t *testing.T) {
	tab := newTestTab(t)
	feedString(tab, "\x1b]2;something\x07")
	feedString(tab, "\x1b]2;\x07")
	if got := tab.Title(); got != "" {
		t.Fatalf("title after empty OSC 2 = %q, want cleared", got)
	}
}

// Title tracking must not alter the output path: OSC bytes never reach the
// screen, and the raw stream (including the OSC sequence) is what the ring
// and the recording carry.
func TestOSCTitleKeepsOutputPathContracts(t *testing.T) {
	tab := newTestTab(t)
	feedString(tab, "before\x1b]2;contract\x07after")
	if got := tab.ScreenText(); strings.Contains(got, "contract") {
		t.Fatalf("OSC payload leaked onto the screen: %q", got)
	}
	if got := tab.Dump(0); !bytes.Contains(got, []byte("\x1b]2;contract\x07")) {
		t.Fatalf("ring lost the raw OSC bytes: %q", got)
	}
	if got := tab.Title(); got != "contract" {
		t.Fatalf("title = %q, want %q", got, "contract")
	}
}

// Emoji sequences in OSC 0/2 titles survive whole: ZWJ, variation selectors,
// skin tones, keycaps, regional-indicator flags and tag sequences, including a
// sequence fed byte by byte.
func TestOSCTitleKeepsEmojiSequences(t *testing.T) {
	titles := []string{
		"👨\u200d👩\u200d👧\u200d👦 family",
		"❤\ufe0f build",
		"🇯🇵🇺🇸 flags",
		"👍🏽 ok",
		"1\ufe0f\u20e3 keycap",
		"🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f scotland",
		"中文🙂 mixed",
	}
	for _, title := range titles {
		for _, cmd := range []string{"0", "2"} {
			tab := newTestTab(t)
			feedString(tab, "\x1b]"+cmd+";"+title+"\x07")
			if got := tab.Title(); got != title {
				t.Fatalf("OSC %s title = %q, want %q", cmd, got, title)
			}
			if got := tab.Snapshot().Title; got != title {
				t.Fatalf("OSC %s snapshot title = %q, want %q", cmd, got, title)
			}
		}
	}
	tab := newTestTab(t)
	seq := []byte("\x1b]2;👨\u200d💻 dev\x07")
	for i := range seq {
		tab.Feed(seq[i : i+1])
	}
	if got := tab.Title(); got != "👨\u200d💻 dev" || !utf8.ValidString(got) {
		t.Fatalf("byte-split title = %q", got)
	}
}

// The title stays readable after the tab closes.
func TestOSCTitleReadableAfterClose(t *testing.T) {
	tab := NewTab("t1", "s1", 80, 24, UTF8)
	feedString(tab, "\x1b]2;final title\x07")
	tab.Close()
	if got := tab.Title(); got != "final title" {
		t.Fatalf("title after close = %q", got)
	}
}
