package update

import "fmt"

type PlanKind string

const (
	PlanNSIS    PlanKind = "nsis"
	PlanDMG     PlanKind = "dmg"
	PlanTarball PlanKind = "tarball"
)

type Plan struct {
	Kind   PlanKind
	GOOS   string
	GOARCH string
}

func planFor(goos, goarch string) (Plan, error) {
	switch goos + "/" + goarch {
	case "windows/amd64", "windows/arm64":
		return Plan{Kind: PlanNSIS, GOOS: goos, GOARCH: goarch}, nil
	case "darwin/amd64", "darwin/arm64":
		return Plan{Kind: PlanDMG, GOOS: goos, GOARCH: goarch}, nil
	case "linux/amd64", "linux/arm64":
		return Plan{Kind: PlanTarball, GOOS: goos, GOARCH: goarch}, nil
	}
	return Plan{}, fmt.Errorf("平台 %s/%s 不支持应用内安装", goos, goarch)
}
