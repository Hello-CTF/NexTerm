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

func buildStubBinary(directory, mainSource, goos, goarch, cgo string) (string, error) {
	if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte(mainSource), 0o644); err != nil {
		return "", err
	}
	initCmd := exec.Command("go", "mod", "init", "stub")
	initCmd.Dir = directory
	if out, err := initCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go mod init: %v: %s", err, out)
	}
	output := filepath.Join(directory, "stub-binary")
	buildCmd := exec.Command("go", "build", "-ldflags", "-s -w", "-o", output, ".")
	buildCmd.Dir = directory
	buildGOARCH := goarch
	if goarch == "armv7" {
		buildGOARCH = "arm"
	}
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED="+cgo, "GOOS="+goos, "GOARCH="+buildGOARCH)
	if goarch == "armv7" {
		buildCmd.Env = append(buildCmd.Env, "GOARM=7")
	}
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %v: %s", err, out)
	}
	return output, nil
}

type artifactReport struct {
	Status       string `json:"status"`
	Commit       string `json:"commit"`
	SourceCommit string `json:"source_commit"`
	Dirty        bool   `json:"dirty_source_tree"`
	Assertions   []struct {
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

func writeBinaryManifest(binary, kind, goos, goarch, cgo, version, commit string) error {
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(binaryBytes)
	value := map[string]interface{}{
		"schema_version":    1,
		"kind":              "go-binary",
		"id":                fmt.Sprintf("%s-%s-%s", kind, goos, goarch),
		"platform":          map[string]interface{}{"os": goos, "arch": goarch, "cgo": cgo},
		"tags":              []string{"production"},
		"stripped":          true,
		"version":           version,
		"commit":            commit,
		"source_date_epoch": 1,
		"artifact": map[string]interface{}{
			"size_bytes": len(binaryBytes),
			"sha256":     hex.EncodeToString(digest[:]),
		},
	}
	if kind == "desktop" || kind == "server" {
		if err := writeStubPrivacyRuntime(binary, goos, goarch); err != nil {
			return err
		}
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "ai-runtime-"+goos+"-"+goarch, "runtime.json"))
		if err != nil {
			return err
		}
		runtimeDigest := sha256.Sum256(raw)
		value["privacy_runtime_sha256"] = hex.EncodeToString(runtimeDigest[:])
	}
	manifest, _ := json.Marshal(value)
	return os.WriteFile(binary+".manifest.json", manifest, 0o644)
}

func runEvidenceChecks(root string) []checkResult {
	c := &collector{}
	check := c.check
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

	base, err := os.MkdirTemp("", "release-evidence-")
	if err != nil {
		fatalf("%v", err)
	}
	defer os.RemoveAll(base)
	binary, err := buildStubBinary(base, "package main\n\nfunc main() {}\n", hostGOOS, hostGOARCH, "1")
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
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = root
	statusOut, err := statusCmd.Output()
	if err != nil {
		fatalf("git status --porcelain: %v", err)
	}
	dirty := strings.TrimSpace(string(statusOut)) != ""
	stampedCommit := head
	if dirty {
		stampedCommit += "+dirty"
	}
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
	if err := writeStubPrivacyRuntime(binary, hostGOOS, hostGOARCH); err != nil {
		fatalf("%v", err)
	}

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
	expectedAssertions := []string{"binary-format", "binary-architecture", "cgo-boundary", "stripped-release", "privacy-runtime"}
	allPassed := true
	for _, name := range expectedAssertions {
		if assertions[name] != "passed" {
			allPassed = false
		}
	}
	check("desktop binary evidence assertions pass with the toolchain present",
		passing.rc == 0 && passedReport.Status == "passed" && allPassed &&
			passedReport.Commit == stampedCommit && passedReport.SourceCommit == head && passedReport.Dirty == dirty &&
			passedReport.Artifact.Sha256 == digestHex,
		fmt.Sprintf("rc=%d status=%s assertions=%v commit=%s source_commit=%s dirty=%t stderr tail=%q",
			passing.rc, passedReport.Status, assertions, passedReport.Commit, passedReport.SourceCommit, passedReport.Dirty, tail(passing.stderr, 300)))

	writeManifest := func(commit string) {
		if err := writeBinaryManifest(binary, "desktop", hostGOOS, hostGOARCH, cgo, version, commit); err != nil {
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

	desktopVersion := version
	if os.Getenv("NEXTERM_RELEASE_VERSION") == "" {
		desktopVersion = "1.2.3"
	}
	c.results = append(c.results, runProductionDependencyChecks(root)...)
	c.results = append(c.results, runDesktopDistChecks(root, desktopVersion, hostGOOS, hostGOARCH)...)
	return c.results
}

// The evidence fixture validates packaging contracts without loading inference.
// The stub library reuses the host binary so architecture checks still apply.
func writeStubPrivacyRuntime(binary, goos, goarch string) error {
	directory := filepath.Join(filepath.Dir(binary), "ai-runtime-"+goos+"-"+goarch)
	library := map[string]string{
		"linux":   "libonnxruntime.so",
		"darwin":  "libonnxruntime.dylib",
		"windows": "onnxruntime.dll",
	}[goos]
	data, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(directory); err != nil {
		return err
	}
	files := map[string]string{}
	for _, name := range []string{library, "licenses/ASTRLINK-LICENSE", "licenses/ASTRLINK-NOTICE.md", "licenses/ONNX-RUNTIME-LICENSE", "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES"} {
		file := filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			return err
		}
		content := data
		if strings.HasPrefix(name, "licenses/") {
			content = []byte("fixture license")
		}
		if err := os.WriteFile(file, content, 0644); err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		files[name] = hex.EncodeToString(digest[:])
	}
	manifest, err := json.Marshal(map[string]interface{}{"abi": 2, "platform": goos, "arch": goarch, "onnxRuntime": "1.23.2", "files": files})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "runtime.json"), manifest, 0644)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
