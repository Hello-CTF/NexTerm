package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var sizeRuleMarkers = []string{
	"rust.informational = true",
	`rust.rule = "historical reference only; never a pass/fail gate"`,
	"custom.informational = true",
	`custom.rule = "full-Eino size impact reporting only; never a pass/fail gate"`,
	"stay informational and never gate release",
}

func whichOn(pathEnv, name string) bool {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

func pathWithoutGo() string {
	var entries []string
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(entry, "go")); err == nil {
			continue
		}
		entries = append(entries, entry)
	}
	return strings.Join(entries, string(os.PathListSeparator))
}

func envWithoutGo(shimDir string) ([]string, error) {
	pathEnv := pathWithoutGo()
	if !whichOn(pathEnv, "node") {
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, fmt.Errorf("node is required for the desktop evidence checks")
		}
		if err := os.Symlink(node, filepath.Join(shimDir, "node")); err != nil {
			return nil, err
		}
		pathEnv = shimDir + string(os.PathListSeparator) + pathEnv
	}
	env := os.Environ()
	for i, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			env[i] = "PATH=" + pathEnv
			return env, nil
		}
	}
	return append(env, "PATH="+pathEnv), nil
}

func buildStubDesktopBinary(directory string) (string, error) {
	if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		return "", err
	}
	initCmd := exec.Command("go", "mod", "init", "stub")
	initCmd.Dir = directory
	if out, err := initCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go mod init: %v: %s", err, out)
	}
	buildCmd := exec.Command("go", "build", "-ldflags", "-s -w", "-o", "stub-desktop", ".")
	buildCmd.Dir = directory
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %v: %s", err, out)
	}
	return filepath.Join(directory, "stub-desktop"), nil
}

type artifactReport struct {
	Status     string `json:"status"`
	Commit     string `json:"commit"`
	Assertions []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"assertions"`
	Artifact struct {
		Sha256 string `json:"sha256"`
	} `json:"artifact"`
}

func readArtifactReport(path string) (artifactReport, map[string]string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return artifactReport{}, nil, false
	}
	var report artifactReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return artifactReport{}, nil, false
	}
	assertions := map[string]string{}
	for _, item := range report.Assertions {
		assertions[item.ID] = item.Status
	}
	return report, assertions, true
}

func runEvidenceChecks(in inputs) []checkResult {
	c := &collector{}
	check := c.check
	root := in.root
	release := in.release

	installStep := findStep(asMap(jobsOf(release)["desktop"]), "Install the pinned Wails CLI")
	script := strings.ReplaceAll(stepRun(installStep), "${{ steps.wails-cache.outputs.cache-hit }}", "true")
	base, err := os.MkdirTemp("", "reuse-wails-")
	if err != nil {
		fatalf("%v", err)
	}
	home := filepath.Join(base, "home")
	wailsDir := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(wailsDir, 0o755); err != nil {
		fatalf("%v", err)
	}
	wails := filepath.Join(wailsDir, "wails3")
	if err := os.WriteFile(wails, []byte("#!/usr/bin/env bash\necho wails3 v3.0.0-beta.27\n"), 0o755); err != nil {
		fatalf("%v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "ghpath"), nil, 0o644); err != nil {
		fatalf("%v", err)
	}
	hidden := runScript(script, base, []string{
		"HOME=" + home,
		"PATH=/usr/bin:/bin",
		"GITHUB_PATH=" + filepath.Join(base, "ghpath"),
	})
	check("wails3 stays invisible to the install step when only GITHUB_PATH is extended",
		hidden.rc != 0, fmt.Sprintf("rc=%d stdout=%q stderr=%q", hidden.rc, hidden.stdout, hidden.stderr))
	visible := runScript(script, base, []string{
		"HOME=" + home,
		"PATH=" + wailsDir + ":/usr/bin:/bin",
		"GITHUB_PATH=" + filepath.Join(base, "ghpath"),
	})
	check("install step passes when the restored wails bin dir is already on PATH",
		visible.rc == 0 && strings.Contains(visible.stdout, "wails3 v3.0.0-beta.27"),
		fmt.Sprintf("rc=%d stderr=%q", visible.rc, visible.stderr))
	os.RemoveAll(base)

	buildText, err := os.ReadFile(filepath.Join(root, "scripts", "build.mjs"))
	if err != nil {
		fatalf("%v", err)
	}
	buildTextStr := string(buildText)
	check("embedded dist assertions stay wired into the desktop release report",
		strings.Contains(buildTextStr, `requireEmbedded: kind === "desktop" && release`) &&
			strings.Contains(buildTextStr, `assertion("embedded-index"`) &&
			strings.Contains(buildTextStr, `assertion("embedded-script"`),
		"build.mjs no longer wires embedded dist assertions into the desktop release report")

	var missingMarkers []string
	for _, marker := range sizeRuleMarkers {
		if !strings.Contains(buildTextStr, marker) {
			missingMarkers = append(missingMarkers, marker)
		}
	}
	check("build.mjs keeps Rust and custom-Go size comparisons informational, never a gate",
		len(missingMarkers) == 0, fmt.Sprintf("missing markers: %v", missingMarkers))

	hostGOOS := runtime.GOOS
	hostGOARCH := runtime.GOARCH
	if (hostGOOS != "darwin" && hostGOOS != "linux") || (hostGOARCH != "amd64" && hostGOARCH != "arm64") {
		check("desktop evidence checks require a darwin/linux amd64/arm64 host", false,
			fmt.Sprintf("host=%s/%s", hostGOOS, hostGOARCH))
		return c.results
	}
	if _, err := exec.LookPath("go"); err != nil {
		check("desktop evidence checks require a Go toolchain", false, "go is not on PATH")
		return c.results
	}

	base, err = os.MkdirTemp("", "reuse-evidence-")
	if err != nil {
		fatalf("%v", err)
	}
	defer os.RemoveAll(base)
	binary, err := buildStubDesktopBinary(base)
	if err != nil {
		fatalf("%v", err)
	}
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		fatalf("%v", err)
	}
	digest := sha256.Sum256(binaryBytes)
	digestHex := hex.EncodeToString(digest[:])
	headCmd := exec.Command("git", "rev-parse", "HEAD")
	headCmd.Dir = root
	headOut, err := headCmd.Output()
	if err != nil {
		fatalf("git rev-parse HEAD: %v", err)
	}
	head := strings.TrimSpace(string(headOut))
	var wailsConfig struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	wailsRaw, err := os.ReadFile(filepath.Join(root, "wails.json"))
	if err != nil {
		fatalf("%v", err)
	}
	if err := json.Unmarshal(wailsRaw, &wailsConfig); err != nil {
		fatalf("%v", err)
	}
	version := os.Getenv("NEXTERM_RELEASE_VERSION")
	if version == "" {
		version = wailsConfig.Info.Version
	}
	cgo := "1"

	cli := []string{"node", filepath.Join(root, "scripts", "build.mjs"), "report", "--kind=desktop",
		"--os=" + hostGOOS, "--arch=" + hostGOARCH, "--cgo=" + cgo,
		"--file=" + binary, "--require-evidence"}
	reportPath := binary + ".artifact.json"

	noGoEnv, err := envWithoutGo(base)
	if err != nil {
		fatalf("%v", err)
	}
	closedCmd := exec.Command(cli[0], cli[1:]...)
	closedCmd.Dir = root
	closedCmd.Env = noGoEnv
	closed := runCmd(closedCmd)
	_, closedAssertions, _ := readArtifactReport(reportPath)
	check("desktop package-only evidence fails closed without go on PATH",
		closed.rc != 0 && closedAssertions["cgo-boundary"] == "failed",
		fmt.Sprintf("rc=%d assertions=%v stderr tail=%q", closed.rc, closedAssertions, tail(closed.stderr, 300)))

	passingCmd := exec.Command(cli[0], cli[1:]...)
	passingCmd.Dir = root
	passing := runCmd(passingCmd)
	passedReport, assertions, _ := readArtifactReport(reportPath)
	expectedAssertions := []string{"binary-format", "binary-architecture", "cgo-boundary", "stripped-release"}
	allPassed := true
	for _, name := range expectedAssertions {
		if assertions[name] != "passed" {
			allPassed = false
		}
	}
	check("desktop binary evidence assertions pass with the toolchain present",
		passing.rc == 0 && passedReport.Status == "passed" && allPassed &&
			passedReport.Commit == head && passedReport.Artifact.Sha256 == digestHex,
		fmt.Sprintf("rc=%d status=%s assertions=%v commit=%s stderr tail=%q",
			passing.rc, passedReport.Status, assertions, passedReport.Commit, tail(passing.stderr, 300)))

	manifestPath := binary + ".manifest.json"
	writeManifest := func(commit string) {
		manifest, _ := json.Marshal(map[string]interface{}{
			"schema_version":    1,
			"kind":              "go-binary",
			"id":                fmt.Sprintf("desktop-%s-%s", hostGOOS, hostGOARCH),
			"platform":          map[string]interface{}{"os": hostGOOS, "arch": hostGOARCH, "cgo": cgo},
			"tags":              []string{"production"},
			"stripped":          true,
			"version":           version,
			"commit":            commit,
			"source_date_epoch": 1,
			"artifact": map[string]interface{}{
				"size_bytes": len(binaryBytes),
				"sha256":     digestHex,
			},
		})
		if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
			fatalf("%v", err)
		}
	}

	verifier := []string{"node", filepath.Join(root, ".github", "scripts", "verify-reused-binary.mjs"), binary,
		fmt.Sprintf("desktop-%s-%s", hostGOOS, hostGOARCH), hostGOOS, hostGOARCH, cgo}
	writeManifest(strings.Repeat("0", 40))
	mismatchCmd := exec.Command(verifier[0], verifier[1:]...)
	mismatchCmd.Dir = root
	mismatch := runCmd(mismatchCmd)
	check("verify-reused-binary rejects a commit mismatch",
		mismatch.rc != 0 && strings.Contains(mismatch.stderr, "commit"),
		fmt.Sprintf("rc=%d stderr=%q", mismatch.rc, mismatch.stderr))
	writeManifest(head)
	acceptedCmd := exec.Command(verifier[0], verifier[1:]...)
	acceptedCmd.Dir = root
	accepted := runCmd(acceptedCmd)
	check("verify-reused-binary accepts the byte-identical manifest",
		accepted.rc == 0, fmt.Sprintf("rc=%d stderr=%q", accepted.rc, accepted.stderr))

	return c.results
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func runUploadChecks(root string) []checkResult {
	c := &collector{}
	uploadTest := filepath.Join(root, ".github", "scripts", "release-upload.test.mjs")
	if _, err := os.Stat(uploadTest); err != nil {
		c.check("release-upload stub tests exist", false, uploadTest)
		return c.results
	}
	cmd := exec.Command("node", "--test", uploadTest)
	cmd.Dir = root
	result := runCmd(cmd)
	detail := ""
	if result.rc != 0 {
		detail = fmt.Sprintf("rc=%d stdout tail=%q stderr tail=%q", result.rc, tail(result.stdout, 400), tail(result.stderr, 400))
	}
	c.check("release-upload stub tests pass", result.rc == 0, detail)
	return c.results
}
