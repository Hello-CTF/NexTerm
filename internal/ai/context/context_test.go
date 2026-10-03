package aicontext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderIncludesScreenAndStablePrompt(t *testing.T) {
	t.Parallel()
	bundle := Bundle{Session: &SessionBrief{Name: "web", Kind: "ssh"}, Screen: "visible", Tail: []string{"tail"}, Recon: "recon", Selection: "selection"}
	rendered := Render(bundle)
	for _, expected := range []string{"[终端当前屏幕]", "visible", "[终端最近输出]", "[环境侦察快照]", "[用户选中的文本]"} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("missing %q in %q", expected, rendered)
		}
	}
	first, second := SystemPrompt(), SystemPrompt()
	if first != second || first == "" {
		t.Fatal("stable prompt changed or empty")
	}
}

func TestBudgetTrimmingDropsLowPriorityAndStaysValid(t *testing.T) {
	t.Parallel()
	bundle := Bundle{Screen: strings.Repeat("屏", 20000), Tail: []string{strings.Repeat("t", 30000)}, Recon: strings.Repeat("r", 30000), Selection: strings.Repeat("s", 30000)}
	TrimToBudget(&bundle)
	rendered := Render(bundle)
	if len(rendered) > BudgetBytes {
		t.Fatalf("rendered size = %d, budget = %d", len(rendered), BudgetBytes)
	}
	if !utf8.ValidString(rendered) {
		t.Fatal("invalid UTF-8 after trimming")
	}
	if bundle.Selection != "" {
		t.Fatal("selection should be dropped first")
	}
	if bundle.Screen == "" {
		t.Fatal("screen should have highest priority")
	}
}
