package tools

import (
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func TestR1TerminalInputBufferReconstructsWrappedCommand(t *testing.T) {
	screen := Screen{Text: "$ rm -rf /home/user\n; ls", CursorRow: 1}
	buffered, _ := TerminalInputCursor(screen)
	if buffered != "rm -rf /home/user; ls" {
		t.Fatalf("wrapped buffer = %q", buffered)
	}
	if got := guard.ClassifySendKeysWithBuffer("", buffered, true, nil); got.Risk != guard.Forbidden {
		t.Fatalf("wrapped command ruling = %v", got)
	}
}

func TestR1MidlineCursorKeepsEntireSubmission(t *testing.T) {
	screen := Screen{Text: "$ ls; rm -rf /home/user", CursorCol: 4}
	buffered, cursor := TerminalInputCursor(screen)
	if buffered != "ls; rm -rf /home/user" || cursor != 2 {
		t.Fatalf("midline input = %q cursor %d", buffered, cursor)
	}
	if got := guard.ClassifySendKeysWithCursor("", buffered, cursor, true, nil); got.Risk != guard.Forbidden {
		t.Fatalf("midline submission ruling = %v", got)
	}
}

func TestR1ZeroCursorColumnIsPreserved(t *testing.T) {
	screen := Screen{Text: "$ rm -rf /home\nX", CursorRow: 1, CursorCol: 0}
	buffered, cursor := TerminalInputCursor(screen)
	if buffered != "rm -rf /homeX" || cursor != 12 {
		t.Fatalf("zero-column input = %q cursor %d", buffered, cursor)
	}
	if got := guard.ClassifySendKeysWithCursor("<delete><enter>", buffered, cursor, false, nil); got.Risk != guard.Forbidden {
		t.Fatalf("zero-column deletion ruling = %v", got)
	}
}

func TestR3PendingInputIncludesBelowRowsAndCommonPrompt(t *testing.T) {
	screen := Screen{Text: "$ echo \n; rm -rf /home/user", CursorRow: 0}
	buffered, _ := TerminalInputCursor(screen)
	if !strings.Contains(buffered, "rm -rf /home/user") {
		t.Fatalf("pending suffix missing from %q", buffered)
	}
	if got := guard.ClassifySendKeysWithBuffer("ls<enter>", buffered, false, nil); got.Risk != guard.Forbidden {
		t.Fatalf("below-row pending command ruling = %v", got)
	}
	common := Screen{Text: "root@host:~# rm -rf /home/user"}
	buffered, _ = TerminalInputCursor(common)
	if buffered != "rm -rf /home/user" {
		t.Fatalf("common prompt buffer = %q", buffered)
	}
}
