package aicontext

import (
	"strings"
	"testing"
)

func TestBudgetHandlesOversizedSessionAndSingleTailLine(t *testing.T) {
	t.Parallel()
	bundle := Bundle{Session: &SessionBrief{Name: strings.Repeat("n", BudgetBytes), Kind: "ssh"}, Tail: []string{strings.Repeat("t", BudgetBytes)}}
	TrimToBudget(&bundle)
	if size := len(Render(bundle)); size > BudgetBytes {
		t.Fatalf("size=%d", size)
	}
}
