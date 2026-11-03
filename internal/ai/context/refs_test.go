package aicontext

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func refsBuilder() *Builder {
	return NewBuilder(Dependencies{
		Session: func(_ context.Context, id string) (SessionBrief, error) {
			if id != "s1" {
				return SessionBrief{}, errors.New("unknown session")
			}
			return SessionBrief{Name: "生产机", Kind: "ssh", Host: "10.0.0.1:22", Username: "root", CWD: "/srv"}, nil
		},
		Screen: func(_ context.Context, id string) (tools.Screen, error) {
			if id == "tab-1" {
				return tools.Screen{Text: "prompt$ ls\nfile-a\nfile-b"}, nil
			}
			return tools.Screen{}, errors.New("unknown tab")
		},
	})
}

func TestRenderRefsInjectsRealContent(t *testing.T) {
	rendered := refsBuilder().RenderRefs(context.Background(), []RefSpec{
		{Kind: "asset", ID: "s1", Label: "生产机"},
		{Kind: "tab", ID: "tab-1", Label: "终端 1"},
		{Kind: "tab", ID: "tab-x", Label: "坏标签"},
		{Kind: "file", Label: "nginx.conf", SessionID: "s1", Path: "/etc/nginx/nginx.conf"},
	}, func(_ context.Context, sessionID, path string) (string, error) {
		if sessionID == "s1" && path == "/etc/nginx/nginx.conf" {
			return "server {\n}\n", nil
		}
		return "", errors.New("not found")
	})
	for _, want := range []string{
		"[Reference: session 生产机]",
		"- Host: 10.0.0.1:22",
		"- Current Directory: /srv",
		"[Reference: terminal screen of 终端 1]",
		"prompt$ ls",
		"- 坏标签 (unable to read the terminal screen)",
		"[Reference: file /etc/nginx/nginx.conf]",
		"server {",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered missing %q:\n%s", want, rendered)
		}
	}
	if refsBuilder().RenderRefs(context.Background(), nil, nil) != "" {
		t.Fatal("empty refs must render empty")
	}
}

func TestRenderRefsTruncatesScreenTailAndFileHead(t *testing.T) {
	builder := NewBuilder(Dependencies{
		Screen: func(context.Context, string) (tools.Screen, error) {
			return tools.Screen{Text: strings.Repeat("a", RefScreenRunes) + strings.Repeat("b", RefScreenRunes)}, nil
		},
	})
	rendered := builder.RenderRefs(context.Background(), []RefSpec{
		{Kind: "tab", ID: "tab-1", Label: "t"},
		{Kind: "file", Label: "f", SessionID: "s1", Path: "/big"},
	}, func(context.Context, string, string) (string, error) {
		return strings.Repeat("x", RefFileRunes) + strings.Repeat("y", RefFileRunes), nil
	})
	if !strings.Contains(rendered, "[Earlier content truncated]\n"+strings.Repeat("b", RefScreenRunes)) {
		t.Fatalf("screen must keep the latest tail:\n%s", rendered[:200])
	}
	if strings.Contains(rendered, strings.Repeat("a", RefScreenRunes)) {
		t.Fatal("screen head must be truncated away")
	}
	if !strings.Contains(rendered, strings.Repeat("x", 100)) || strings.Contains(rendered, strings.Repeat("y", 100)) {
		t.Fatal("file must keep the head and drop the tail")
	}
	if !strings.Contains(rendered, "[Content truncated]") {
		t.Fatal("file truncation marker missing")
	}
}

func TestRenderRefsBudgetAndCountCaps(t *testing.T) {
	readFile := func(context.Context, string, string) (string, error) {
		return strings.Repeat("z", RefFileRunes), nil
	}
	refs := make([]RefSpec, 0, RefMaxCount+1)
	for i := 0; i < RefMaxCount+1; i++ {
		refs = append(refs, RefSpec{Kind: "file", Label: "f", SessionID: "s1", Path: "/f"})
	}
	rendered := NewBuilder(Dependencies{}).RenderRefs(context.Background(), refs, readFile)
	if count := strings.Count(rendered, "[Reference: file /f]"); count > RefMaxCount {
		t.Fatalf("ref count cap ignored: %d", count)
	}
	if got := len([]rune(rendered)); got > RefTotalRunes+200 {
		t.Fatalf("total budget exceeded: %d runes", got)
	}
	if count := strings.Count(rendered, strings.Repeat("z", RefFileRunes)); count > 2 {
		t.Fatalf("sections must shrink under budget, full sections: %d", count)
	}
}

func TestRenderRefsSkipsUnsupported(t *testing.T) {
	rendered := NewBuilder(Dependencies{}).RenderRefs(context.Background(), []RefSpec{
		{Kind: "unknown", ID: "x", Label: "x"},
		{Kind: "tab", ID: "tab-1", Label: "no screen dep"},
		{Kind: "file", Label: "no reader", Path: "/f"},
	}, nil)
	if rendered != "" {
		t.Fatalf("unsupported refs must render empty, got %q", rendered)
	}
}

func TestRenderRefsTextKind(t *testing.T) {
	builder := NewBuilder(Dependencies{})
	rendered := builder.RenderRefs(context.Background(), []RefSpec{
		{Kind: "text", Label: "ls -la\n(exit 0)", Text: "total 8\nfile-a"},
	}, nil)
	if rendered != "[Reference: command output ls -la (exit 0)]\ntotal 8\nfile-a\n" {
		t.Fatalf("unexpected render: %q", rendered)
	}
}

func TestRenderRefsTextKindEmptyAndUnlabeled(t *testing.T) {
	builder := NewBuilder(Dependencies{})
	rendered := builder.RenderRefs(context.Background(), []RefSpec{
		{Kind: "text", ID: "blk-1", Text: " \n\t"},
	}, nil)
	if rendered != "- blk-1 (empty)\n" {
		t.Fatalf("unexpected render: %q", rendered)
	}
}

func TestRenderRefsTextKindKeepsTailWithinScreenLimit(t *testing.T) {
	text := strings.Repeat("a", RefScreenRunes) + strings.Repeat("b", RefScreenRunes)
	rendered := NewBuilder(Dependencies{}).RenderRefs(context.Background(), []RefSpec{
		{Kind: "text", Label: "t", Text: text},
	}, nil)
	want := "[Reference: command output t]\n[Earlier content truncated]\n" + strings.Repeat("b", RefScreenRunes) + "\n"
	if rendered != want {
		t.Fatalf("rendered %d runes, want the exact tail section of %d runes", len([]rune(rendered)), len([]rune(want)))
	}
}

func TestRenderRefsTextKindHonorsRemainingBudget(t *testing.T) {
	refs := []RefSpec{
		{Kind: "text", Label: "one", Text: strings.Repeat("a", RefScreenRunes)},
		{Kind: "text", Label: "two", Text: strings.Repeat("b", RefScreenRunes)},
		{Kind: "text", Label: "three", Text: strings.Repeat("c", RefScreenRunes)},
		{Kind: "text", Label: "four", Text: strings.Repeat("d", RefScreenRunes)},
	}
	rendered := NewBuilder(Dependencies{}).RenderRefs(context.Background(), refs, nil)
	// The first two sections leave 3934 of the 12000 runes; the third is cut to that tail and exhausts the
	// budget, so the fourth is not rendered at all.
	want := "[Reference: command output one]\n" + strings.Repeat("a", RefScreenRunes) + "\n" +
		"[Reference: command output two]\n" + strings.Repeat("b", RefScreenRunes) + "\n" +
		"[Reference: command output three]\n[Earlier content truncated]\n" + strings.Repeat("c", 3934) + "\n"
	if rendered != want {
		t.Fatalf("rendered %d runes, want %d", len([]rune(rendered)), len([]rune(want)))
	}
}

func TestRenderRefsLabelsStayOnOneLine(t *testing.T) {
	rendered := NewBuilder(Dependencies{}).RenderRefs(context.Background(), []RefSpec{
		{Kind: "text", Label: " ls\r\n\t\x1b[31mred\u0085  x\u2028y\x00 ", Text: "out"},
		{Kind: "text", ID: "blk-2", Label: "\r\n\t\x1b\u009b", Text: "out2"},
	}, nil)
	want := "[Reference: command output ls [31mred x y]\nout\n" +
		"[Reference: command output blk-2]\nout2\n"
	if rendered != want {
		t.Fatalf("rendered %q, want %q", rendered, want)
	}
	for _, r := range strings.ReplaceAll(rendered, "\n", "") {
		if unicode.IsControl(r) {
			t.Fatalf("control character %U survived in %q", r, rendered)
		}
	}
}

func TestRenderRefsIgnoresTextOnOtherKinds(t *testing.T) {
	rendered := refsBuilder().RenderRefs(context.Background(), []RefSpec{
		{Kind: "tab", ID: "tab-1", Label: "t", Text: "INJECTED-TEXT"},
		{Kind: "asset", ID: "s1", Label: "a", Text: "INJECTED-TEXT"},
	}, nil)
	if strings.Contains(rendered, "INJECTED-TEXT") || rendered == "" {
		t.Fatalf("text field must only affect the text kind: %q", rendered)
	}
}
