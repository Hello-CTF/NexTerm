package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// runProductionDependencyChecks guards the release build composition with go
// list: the static server/agent/helper release commands must never pull in the
// Wails desktop UI module, while the desktop command must keep it. A leak
// would either break the CGO_ENABLED=0 static builds or silently ship the
// desktop UI stack inside a server artifact.
func runProductionDependencyChecks(root string) []checkResult {
	c := &collector{}
	check := c.check
	if _, err := exec.LookPath("go"); err != nil {
		check("production dependency checks require a Go toolchain", false, "go is not on PATH")
		return c.results
	}
	listDeps := func(goos, goarch, cgo, target, overlay string) ([]string, error) {
		args := []string{"list", "-deps", "-tags", "production", "-mod=readonly"}
		if overlay != "" {
			args = append(args, "-overlay="+overlay)
		}
		args = append(args, target)
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED="+cgo, "GOWORK=off")
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list -deps %s: %w", target, err)
		}
		var deps []string
		for _, line := range strings.Split(string(output), "\n") {
			if line != "" {
				deps = append(deps, line)
			}
		}
		return deps, nil
	}
	const wailsModule = "github.com/wailsapp/wails"
	for _, target := range []struct{ name, goos, goarch, cgo string }{
		{"nexterm-server", "linux", "amd64", "1"},
		{"nexterm-agent", "linux", "amd64", "0"},
		{"nexterm-helper", "linux", "amd64", "0"},
	} {
		deps, err := listDeps(target.goos, target.goarch, target.cgo, "./cmd/"+target.name, "")
		if err != nil {
			check(fmt.Sprintf("production %s dependency list resolves", target.name), false, err.Error())
			continue
		}
		var leaked []string
		for _, dep := range deps {
			if strings.HasPrefix(dep, wailsModule) {
				leaked = append(leaked, dep)
			}
		}
		sort.Strings(leaked)
		check(fmt.Sprintf("production %s excludes the Wails desktop module", target.name), len(leaked) == 0,
			fmt.Sprintf("wails dependencies: %v", leaked))
	}
	// The production-tagged desktop package embeds all:dist, which go list
	// evaluates; supply a placeholder dist through an overlay so the checkout
	// stays untouched.
	overlayDir, err := os.MkdirTemp("", "nexterm-desktop-deps-*")
	if err != nil {
		check("production nexterm-desktop dependency list resolves", false, err.Error())
		return c.results
	}
	defer func() { _ = os.RemoveAll(overlayDir) }()
	placeholder := filepath.Join(overlayDir, "index.html")
	if err := os.WriteFile(placeholder, []byte("<html>placeholder</html>\n"), 0o600); err != nil {
		check("production nexterm-desktop dependency list resolves", false, err.Error())
		return c.results
	}
	overlayJSON, err := json.Marshal(map[string]any{
		"Replace": map[string]string{filepath.Join(root, "cmd", "nexterm-desktop", "dist", "index.html"): placeholder},
	})
	if err != nil {
		check("production nexterm-desktop dependency list resolves", false, err.Error())
		return c.results
	}
	overlayPath := filepath.Join(overlayDir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlayJSON, 0o600); err != nil {
		check("production nexterm-desktop dependency list resolves", false, err.Error())
		return c.results
	}
	desktopDeps, err := listDeps(runtime.GOOS, runtime.GOARCH, "1", "./cmd/nexterm-desktop", overlayPath)
	if err != nil {
		check("production nexterm-desktop dependency list resolves", false, err.Error())
		return c.results
	}
	wailsPresent := false
	for _, dep := range desktopDeps {
		if strings.HasPrefix(dep, wailsModule+"/v3") {
			wailsPresent = true
			break
		}
	}
	check("production nexterm-desktop keeps the Wails desktop module", wailsPresent,
		fmt.Sprintf("dependency count: %d", len(desktopDeps)))
	return c.results
}
