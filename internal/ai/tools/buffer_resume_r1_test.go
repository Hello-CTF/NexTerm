package tools

import (
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
)

func TestR1TerminalInputBufferReconstructsWrappedCommand(t *testing.T) {
	screen := Screen{Text: "$ rm -rf /home/user\n; ls", CursorRow: 1}
	buffered := TerminalInputBuffer(screen)
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
