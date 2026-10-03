package guard

import "testing"

func TestR1QuotedStoredFunctionIsNotReadOnly(t *testing.T) {
	t.Parallel()
	if got := ClassifySQL("SELECT `mutating_stored_function`()", nil); got.Risk != NeedsConfirm {
		t.Fatalf("quoted stored function = %v, want NeedsConfirm", got)
	}
	if got := ClassifySQL("SELECT `COUNT`(*)", nil); got.Risk != NeedsConfirm {
		t.Fatalf("quoted allowlisted function is not implicitly trusted: %v", got)
	}
	if got := ClassifySQL(`SELECT "mutating_stored_function"()`, nil); got.Risk != NeedsConfirm {
		t.Fatalf("double-quoted stored function = %v, want NeedsConfirm", got)
	}
}

func TestR1DashDashRequiresCommentWhitespace(t *testing.T) {
	t.Parallel()
	if got := ClassifySQL("SELECT 1--1 INTO OUTFILE '/tmp/nexterm-m23'", nil); got.Risk != NeedsConfirm {
		t.Fatalf("dash-dash outfile = %v, want NeedsConfirm", got)
	}
	if got := ClassifySQL("SELECT 1-- comment", nil); got.Risk != Safe {
		t.Fatalf("valid dash-dash comment = %v, want Safe", got)
	}
}
