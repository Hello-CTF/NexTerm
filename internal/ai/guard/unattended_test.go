package guard

import (
	"context"
	"strings"
	"testing"
)

func TestUnattendedDecisionMatrix(t *testing.T) {
	config := Config{Mode: Unattended}
	memory := NewMemory()
	memory.Add(KindWriteFS)
	memory.Add(KindSudo)
	cases := []struct {
		name   string
		ruling Ruling
		action Action
	}{
		{"safe allows", Allow(), ActionAllow},
		{"needs confirm denies even when remembered", Confirm(KindWriteFS, "写入或编辑文件"), ActionDeny},
		{"unknowable denies", Indeterminate("动态输入"), ActionDeny},
		{"danger denies", Dangerous("删除容器需要始终确认"), ActionDeny},
		{"forbidden denies", Deny("关键路径"), ActionDeny},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision := Decide(config, test.ruling, memory)
			if decision.Action != test.action {
				t.Fatalf("action = %s; want %s (decision %+v)", decision.Action, test.action, decision)
			}
		})
	}
}

func TestUnattendedDenyCarriesHonestReason(t *testing.T) {
	decision := Decide(Config{Mode: Unattended}, Confirm(KindWriteFS, "写入或编辑文件"), nil)
	if decision.Action != ActionDeny {
		t.Fatalf("action = %s; want deny", decision.Action)
	}
	if !strings.Contains(decision.Ruling.Reason, "写入或编辑文件") || !strings.Contains(decision.Reason, "无人值守") {
		t.Fatalf("deny reason hides its cause: %+v", decision)
	}
	forbidden := Decide(Config{Mode: Unattended}, Deny("递归删除关键路径"), nil)
	if forbidden.Action != ActionDeny || forbidden.Ruling.Reason != "递归删除关键路径" {
		t.Fatalf("forbidden ruling was rewritten: %+v", forbidden)
	}
}

func TestUnattendedModeSurvivesNormalization(t *testing.T) {
	if got := (Config{Mode: Unattended}).Normalized().Mode; got != Unattended {
		t.Fatalf("normalized mode = %q; want unattended", got)
	}
	if got := (Config{Mode: "yolo"}).Normalized().Mode; got != ReadWrite {
		t.Fatalf("unknown mode normalized to %q; want read_write", got)
	}
}

func TestUnattendedContextMarker(t *testing.T) {
	if UnattendedFrom(context.Background()) {
		t.Fatal("plain context reported unattended")
	}
	marked := WithUnattended(context.Background())
	if !UnattendedFrom(marked) {
		t.Fatal("marked context lost the unattended marker")
	}
	derived := context.WithValue(marked, struct{}{}, "x")
	if !UnattendedFrom(derived) {
		t.Fatal("marker must survive derived contexts")
	}
}
