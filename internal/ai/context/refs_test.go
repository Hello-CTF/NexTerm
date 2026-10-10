package aicontext

import (
	"context"
	"errors"
	"strings"
	"testing"

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
		"[引用：会话 生产机]",
		"- 主机：10.0.0.1:22",
		"- 当前目录：/srv",
		"[引用：终端 1 的终端屏幕]",
		"prompt$ ls",
		"- 坏标签（无法读取终端屏幕）",
		"[引用：文件 /etc/nginx/nginx.conf]",
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
	if !strings.Contains(rendered, "[前文已截断]\n"+strings.Repeat("b", RefScreenRunes)) {
		t.Fatalf("screen must keep the latest tail:\n%s", rendered[:200])
	}
	if strings.Contains(rendered, strings.Repeat("a", RefScreenRunes)) {
		t.Fatal("screen head must be truncated away")
	}
	if !strings.Contains(rendered, strings.Repeat("x", 100)) || strings.Contains(rendered, strings.Repeat("y", 100)) {
		t.Fatal("file must keep the head and drop the tail")
	}
	if !strings.Contains(rendered, "[内容已截断]") {
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
	if count := strings.Count(rendered, "[引用：文件 /f]"); count > RefMaxCount {
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
