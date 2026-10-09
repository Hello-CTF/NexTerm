package update

import (
	"strings"
	"testing"
)

func TestPlanForSelectsInstallerPerPlatform(t *testing.T) {
	cases := map[string]PlanKind{
		"windows/amd64": PlanNSIS,
		"windows/arm64": PlanNSIS,
		"darwin/amd64":  PlanDMG,
		"darwin/arm64":  PlanDMG,
		"linux/amd64":   PlanTarball,
		"linux/arm64":   PlanTarball,
	}
	for platform, want := range cases {
		goos, goarch, _ := strings.Cut(platform, "/")
		plan, err := planFor(goos, goarch)
		if err != nil {
			t.Fatalf("planFor(%s): %v", platform, err)
		}
		if plan.Kind != want {
			t.Fatalf("planFor(%s).Kind = %s, want %s", platform, plan.Kind, want)
		}
	}
}

func TestPlanForRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := planFor("plan9", "amd64"); err == nil {
		t.Fatal("unsupported platform must fail")
	}
}
