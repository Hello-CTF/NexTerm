package aicontext

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func TestBuilderInjectsTabBrief(t *testing.T) {
	builder := NewBuilder(Dependencies{
		Screen: func(context.Context, string) (tools.Screen, error) {
			return tools.Screen{Text: "visible"}, nil
		},
		TabDetail: func(_ context.Context, tabID string) (tools.TabDetail, error) {
			return tools.TabDetail{TabID: tabID, SessionID: "s1", Kind: "ssh", Title: "deploy", CWD: "/srv/app", Durable: true}, nil
		},
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	prompt := builder.Build(context.Background(), tools.Scope{SessionID: "s1", TabID: "t1"}, "")
	if prompt.Bundle.Tab == nil {
		t.Fatal("tab brief missing")
	}
	if prompt.Bundle.Tab.Handle != "t/t1/s1" || prompt.Bundle.Tab.Kind != "ssh" || prompt.Bundle.Tab.CWD != "/srv/app" {
		t.Fatalf("brief = %+v", prompt.Bundle.Tab)
	}
	for _, want := range []string{"[Terminal Tab]", "- Handle: t/t1/s1", "- Kind: ssh", "- Title: deploy", "- Current Directory: /srv/app", "- Durable: true"} {
		if !strings.Contains(prompt.Volatile, want) {
			t.Fatalf("volatile missing %q:\n%s", want, prompt.Volatile)
		}
	}
}

func TestBuilderTabBriefDegradesAndTrims(t *testing.T) {
	builder := NewBuilder(Dependencies{
		Screen: func(context.Context, string) (tools.Screen, error) {
			return tools.Screen{Text: "visible"}, nil
		},
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	prompt := builder.Build(context.Background(), tools.Scope{SessionID: "s1", TabID: "t1"}, "")
	if prompt.Bundle.Tab != nil {
		t.Fatalf("unexpected tab brief without dependency: %+v", prompt.Bundle.Tab)
	}
	if strings.Contains(prompt.Volatile, "[Terminal Tab]") {
		t.Fatalf("tab section rendered without dependency:\n%s", prompt.Volatile)
	}

	// Under budget pressure the tab brief drops before the session brief:
	// the session name is sized so dropping the tab section alone suffices.
	bundle := Bundle{
		Now:     time.Unix(0, 0),
		Session: &SessionBrief{Name: strings.Repeat("s", 65390), Kind: "ssh"},
		Tab:     &TabBrief{Handle: "t/t1/s1", Kind: "ssh"},
	}
	TrimToBudget(&bundle)
	if bundle.Tab != nil {
		t.Fatal("tab brief survived budget trim")
	}
	if bundle.Session == nil {
		t.Fatal("session brief dropped before tab brief")
	}
}
