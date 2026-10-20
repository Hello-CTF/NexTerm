package execbatch

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func retainedFill(total int) []byte {
	var builder strings.Builder
	for builder.Len() < total {
		builder.WriteString("0123456789abcdef")
	}
	return []byte(builder.String()[:total])
}

func TestRetainedOutputKeepsSmallStreamWhole(t *testing.T) {
	output := NewRetainedOutput(DefaultOutputRetention)
	output.Write([]byte("hello"))
	output.Write([]byte(" world"))
	snapshot := output.Snapshot()
	if snapshot.Head != "hello world" || snapshot.Tail != "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.TotalBytes != 11 || snapshot.DroppedBytes != 0 || snapshot.Truncated {
		t.Fatalf("snapshot accounting = %+v", snapshot)
	}
}

func TestRetainedOutputBoundsHeadAndTail(t *testing.T) {
	output := NewRetainedOutput(OutputRetention{Head: 8, Tail: 4})
	output.Write(retainedFill(6))
	output.Write(retainedFill(20))
	snapshot := output.Snapshot()
	if snapshot.Head != "01234501" || snapshot.Tail != "0123" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.TotalBytes != 26 || snapshot.DroppedBytes != 14 || !snapshot.Truncated {
		t.Fatalf("snapshot accounting = %+v", snapshot)
	}
}

func TestRetainedOutputExactFitIsNotTruncated(t *testing.T) {
	headOnly := NewRetainedOutput(OutputRetention{Head: 8, Tail: 4})
	headOnly.Write(retainedFill(8))
	if snapshot := headOnly.Snapshot(); snapshot.Truncated || snapshot.Tail != "" || snapshot.DroppedBytes != 0 {
		t.Fatalf("head fit snapshot = %+v", snapshot)
	}

	both := NewRetainedOutput(OutputRetention{Head: 8, Tail: 4})
	both.Write(retainedFill(12))
	snapshot := both.Snapshot()
	if snapshot.Truncated || snapshot.DroppedBytes != 0 || snapshot.Head != "01234567" || snapshot.Tail != "89ab" {
		t.Fatalf("head+tail fit snapshot = %+v", snapshot)
	}
}

func TestRetainedOutputSingleLargeWrite(t *testing.T) {
	output := NewRetainedOutput(OutputRetention{Head: 16, Tail: 16})
	output.Write(retainedFill(256 << 10))
	snapshot := output.Snapshot()
	if snapshot.Head != "0123456789abcdef" || snapshot.Tail != "0123456789abcdef" {
		t.Fatalf("large write snapshot = %+v", snapshot)
	}
	if snapshot.TotalBytes != 256<<10 || snapshot.DroppedBytes != 256<<10-32 || !snapshot.Truncated {
		t.Fatalf("large write accounting = %+v", snapshot)
	}
}

func TestRetainedOutputZeroBounds(t *testing.T) {
	headless := NewRetainedOutput(OutputRetention{Head: 0, Tail: 4})
	headless.Write(retainedFill(10))
	if snapshot := headless.Snapshot(); snapshot.Head != "" || snapshot.Tail != "6789" || snapshot.DroppedBytes != 6 {
		t.Fatalf("headless snapshot = %+v", snapshot)
	}

	tailless := NewRetainedOutput(OutputRetention{Head: 4, Tail: 0})
	tailless.Write(retainedFill(10))
	if snapshot := tailless.Snapshot(); snapshot.Head != "0123" || snapshot.Tail != "" || snapshot.DroppedBytes != 6 {
		t.Fatalf("tailless snapshot = %+v", snapshot)
	}

	discard := NewRetainedOutput(OutputRetention{})
	discard.Write(retainedFill(10))
	if snapshot := discard.Snapshot(); snapshot.Head != "" || snapshot.Tail != "" || snapshot.DroppedBytes != 10 || !snapshot.Truncated {
		t.Fatalf("discard snapshot = %+v", snapshot)
	}
}

func TestRetainedOutputTrimsUTF8Seams(t *testing.T) {
	// Head bound lands inside the three-byte 中; the tail window starts
	// inside another 中.
	output := NewRetainedOutput(OutputRetention{Head: 4, Tail: 4})
	output.Write([]byte("ab中cd"))
	output.Write([]byte("ef中gh"))
	snapshot := output.Snapshot()
	if snapshot.Head != "ab" {
		t.Fatalf("head = %q, want %q", snapshot.Head, "ab")
	}
	if snapshot.Tail != "gh" {
		t.Fatalf("tail = %q, want %q", snapshot.Tail, "gh")
	}
	if !utf8.ValidString(snapshot.Head) || !utf8.ValidString(snapshot.Tail) {
		t.Fatalf("snapshot is not valid UTF-8: %+v", snapshot)
	}
	if snapshot.DroppedBytes != 10 {
		t.Fatalf("dropped = %d, want 10 (%+v)", snapshot.DroppedBytes, snapshot)
	}
}

func TestRetainedOutputDropsInvalidTrailingBytes(t *testing.T) {
	output := NewRetainedOutput(OutputRetention{Head: 8, Tail: 0})
	output.Write([]byte{'a', 'b', 0xff, 0xfe})
	snapshot := output.Snapshot()
	if snapshot.Head != "ab" {
		t.Fatalf("head = %q, want %q", snapshot.Head, "ab")
	}
	if snapshot.DroppedBytes != 2 || !snapshot.Truncated {
		t.Fatalf("accounting = %+v", snapshot)
	}
}

func TestRetainedOutputTotalCountsEveryWrite(t *testing.T) {
	output := NewRetainedOutput(OutputRetention{Head: 4, Tail: 4})
	for range 100 {
		output.Write([]byte("xy"))
	}
	snapshot := output.Snapshot()
	if snapshot.TotalBytes != 200 || snapshot.Head != "xyxy" || snapshot.Tail != "xyxy" || snapshot.DroppedBytes != 192 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	output.Write(nil)
	if output.Snapshot().TotalBytes != 200 {
		t.Fatal("empty write changed the total")
	}
}

func TestTargetOutputRetainsStreamsIndependently(t *testing.T) {
	output := NewTargetOutput(OutputRetention{Head: 4, Tail: 4})
	output.WriteStdout(retainedFill(100))
	output.WriteStderr([]byte("oops"))
	snapshot := output.Snapshot()
	if snapshot.Stdout.Head != "0123" || snapshot.Stdout.Tail != "0123" || snapshot.Stdout.DroppedBytes != 92 {
		t.Fatalf("stdout = %+v", snapshot.Stdout)
	}
	if snapshot.Stderr.Head != "oops" || snapshot.Stderr.Truncated || snapshot.Stderr.DroppedBytes != 0 {
		t.Fatalf("stderr = %+v", snapshot.Stderr)
	}
}
